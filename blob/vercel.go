package blob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// Vercel is a Bucket backed by Vercel Blob.
//
// The API is not formally documented as REST — the published surface is a
// JavaScript SDK — so the request shapes here were read from the SDK source
// and then verified against a live private store. Anything surprising is
// commented at the point it bites, because the next person cannot check it
// against a specification.
type Vercel struct {
	token  string
	client *http.Client

	// storeHost is the per-store download host, e.g.
	// "kwuxh0m1jtk8o96r.private.blob.vercel-storage.com". Derived once from
	// the first write or lookup rather than configured, so a token and a store
	// cannot be mismatched.
	storeHost string
}

const (
	// apiBase is the control plane: writes, deletes, metadata and listing.
	// Content is read from the per-store host instead.
	apiBase = "https://blob.vercel-storage.com"

	// apiVersion pins the response format. The SDK sends this on every
	// request and the server changes its behaviour on it.
	apiVersion = "12"
)

// NewVercel returns a Bucket for the store the token belongs to.
//
// storeHost is the download host for the store. It can be left empty, in which
// case it is learned from the first successful write or metadata lookup — but
// passing it avoids a needless round trip on a cold start, which on serverless
// is every request.
func NewVercel(token, storeHost string) (*Vercel, error) {
	if token == "" {
		return nil, fmt.Errorf("blob: a read-write token is required")
	}
	return &Vercel{
		token:     token,
		storeHost: storeHost,
		client:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// NewVercelFromEnv reads BLOB_READ_WRITE_TOKEN, and BLOB_STORE_HOST when set.
func NewVercelFromEnv() (*Vercel, error) {
	return NewVercel(os.Getenv("BLOB_READ_WRITE_TOKEN"), os.Getenv("BLOB_STORE_HOST"))
}

var _ Bucket = (*Vercel)(nil)

// blobInfo is the metadata the API returns for one object.
type blobInfo struct {
	URL      string `json:"url"`
	Pathname string `json:"pathname"`
	ETag     string `json:"etag"`
	Size     int64  `json:"size"`
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// do performs a request, retrying once on a transport error.
//
// Not for robustness in the abstract: a boot-time listing failed with a bare
// EOF during testing, which left the catalogue unseeded for that instance.
// Retried only where the request is safe to repeat — GET and the idempotent
// listing — never a conditional write, where a repeat could succeed against a
// state the caller has not seen.
func (v *Vercel) do(req *http.Request) (*http.Response, error) {
	resp, err := v.client.Do(req)
	if err == nil || req.Method != http.MethodGet {
		return resp, err
	}
	// One retry. A second failure is a real outage rather than a blip, and
	// stacking retries only turns a fast failure into a slow one.
	time.Sleep(150 * time.Millisecond)
	return v.client.Do(req)
}

func (v *Vercel) authorise(req *http.Request) {
	req.Header.Set("authorization", "Bearer "+v.token)
	req.Header.Set("x-api-version", apiVersion)
}

// contentURL builds the download URL for a key. Pathnames are deterministic
// because every write sets x-add-random-suffix: 0, so a key maps to a URL
// without a lookup — which is what makes this a key-addressed store rather
// than one that has to keep an index of URLs.
func (v *Vercel) contentURL(key string, consistent bool) (string, error) {
	if v.storeHost == "" {
		return "", fmt.Errorf("blob: store host is not known yet")
	}
	u := &url.URL{Scheme: "https", Host: v.storeHost, Path: "/" + key}
	if consistent {
		// Bypasses the CDN so the read comes from origin. Without it a read
		// can return a stale body and ETag; PutIfMatch would still reject the
		// resulting write, so this is not a correctness fix — it prevents
		// conflicts the user would experience as a save failing for no reason.
		u.RawQuery = "cache=0"
	}
	return u.String(), nil
}

// learnHost records the per-store host from a returned blob URL.
func (v *Vercel) learnHost(blobURL string) {
	if v.storeHost != "" || blobURL == "" {
		return
	}
	if u, err := url.Parse(blobURL); err == nil {
		v.storeHost = u.Host
	}
}

func (v *Vercel) Get(ctx context.Context, key string) (Object, error) {
	if v.storeHost == "" {
		// Cold start with no configured host: one metadata call teaches it.
		if _, err := v.head(ctx, key); err != nil {
			return Object{}, err
		}
	}
	target, err := v.contentURL(key, true)
	if err != nil {
		return Object{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Object{}, err
	}
	v.authorise(req)

	resp, err := v.do(req)
	if err != nil {
		return Object{}, fmt.Errorf("blob: getting %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Object{}, ErrNoSuchKey
	case http.StatusForbidden:
		// A private store answers 403 rather than 404 for an absent object,
		// which is the same reasoning as the API returning 404 to non-owners:
		// it refuses to confirm what exists.
		//
		// But it also answers 403 when the store as a whole is blocked, and the
		// two must not be conflated: reading the body is the only way to tell.
		// See ErrStoreBlocked.
		return Object{}, forbiddenError(key, resp)
	default:
		return Object{}, fmt.Errorf("blob: getting %s: %s", key, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Object{}, fmt.Errorf("blob: reading %s: %w", key, err)
	}
	etag := normaliseETag(resp.Header.Get("etag"))
	if etag == "" {
		// The download host does not always echo one; fall back to metadata
		// rather than returning an ETag that cannot be used for a conditional
		// write.
		info, err := v.head(ctx, key)
		if err != nil {
			return Object{}, err
		}
		etag = normaliseETag(info.ETag)
	}
	return Object{Data: data, ETag: etag}, nil
}

// normaliseETag strips the weak-validator prefix.
//
// A CDN may serve `W/"abc"` where the store holds `"abc"` — for a transformed
// or range-served response, for instance. Passing the weak form to x-if-match
// never matches, which presents as every conditional write being refused even
// though nothing changed.
func normaliseETag(etag string) string {
	return strings.TrimPrefix(strings.TrimSpace(etag), "W/")
}

// head fetches metadata for a key without its content.
func (v *Vercel) head(ctx context.Context, key string) (blobInfo, error) {
	// The metadata endpoint takes a URL, not a pathname, so a store host is
	// needed to ask what the store host is. Listing the exact key breaks that
	// circularity on a cold start.
	if v.storeHost == "" {
		infos, err := v.listExpanded(ctx, key, 1)
		if err != nil {
			return blobInfo{}, err
		}
		if len(infos) == 0 || infos[0].Pathname != key {
			return blobInfo{}, ErrNoSuchKey
		}
		v.learnHost(infos[0].URL)
		return infos[0], nil
	}

	target, err := v.contentURL(key, false)
	if err != nil {
		return blobInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/?url="+url.QueryEscape(target), nil)
	if err != nil {
		return blobInfo{}, err
	}
	v.authorise(req)

	resp, err := v.do(req)
	if err != nil {
		return blobInfo{}, fmt.Errorf("blob: head %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return blobInfo{}, ErrNoSuchKey
	}
	if resp.StatusCode == http.StatusForbidden {
		return blobInfo{}, forbiddenError(key, resp)
	}
	if resp.StatusCode != http.StatusOK {
		return blobInfo{}, statusError("head "+key, resp)
	}

	var info blobInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return blobInfo{}, fmt.Errorf("blob: decoding head %s: %w", key, err)
	}
	return info, nil
}

func (v *Vercel) Put(ctx context.Context, key string, data []byte) error {
	return v.put(ctx, key, data, "")
}

func (v *Vercel) PutIfMatch(ctx context.Context, key string, data []byte, etag string) error {
	if etag == "" {
		return fmt.Errorf("blob: PutIfMatch needs an ETag or IfAbsent, not an empty string")
	}
	return v.put(ctx, key, data, etag)
}

func (v *Vercel) put(ctx context.Context, key string, data []byte, etag string) error {
	target := apiBase + "/?pathname=" + url.QueryEscape(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	v.authorise(req)
	req.Header.Set("x-vercel-blob-access", "private")
	req.Header.Set("x-content-type", "application/json")
	// Deterministic pathnames. With a random suffix a key would not map to a
	// URL, and this would stop being a key-addressed store.
	req.Header.Set("x-add-random-suffix", "0")

	switch etag {
	case "":
		// Overwriting is refused by default, which is a guard against
		// accidental clobbering rather than a concurrency control. Unconditional
		// writers want it off.
		req.Header.Set("x-allow-overwrite", "1")
	case IfAbsent:
		req.Header.Set("x-allow-overwrite", "0")
	default:
		req.Header.Set("x-allow-overwrite", "1")
		req.Header.Set("x-if-match", etag)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("blob: putting %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusPreconditionFailed:
		// x-if-match rejected: someone else wrote since we read.
		return ErrPreconditionFailed
	default:
		err := statusError("putting "+key, resp)
		// An IfAbsent write that loses the race is reported as a plain 400
		// with a prose message rather than a 409 or a 412, so the refusal has
		// to be recognised by its text. Verified against a live store; if
		// Vercel rewords it, TestVercelIfAbsent is what fails.
		if etag == IfAbsent && strings.Contains(err.Error(), "already exists") {
			return ErrPreconditionFailed
		}
		return err
	}

	var info blobInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err == nil {
		v.learnHost(info.URL)
	}
	return nil
}

func (v *Vercel) Delete(ctx context.Context, key string) error {
	target, err := v.contentURL(key, false)
	if err != nil {
		// Nothing can have been written without a host, so nothing to delete.
		return nil
	}

	body, err := json.Marshal(map[string]any{"urls": []string{target}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	v.authorise(req)
	req.Header.Set("content-type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("blob: deleting %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Deleting an absent key is not an error: cleanup has to be retryable.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return statusError("deleting "+key, resp)
}

func (v *Vercel) List(ctx context.Context, prefix string) ([]string, error) {
	infos, err := v.listExpanded(ctx, prefix, 0)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Pathname)
	}
	slices.Sort(out)
	return out, nil
}

// listExpanded pages through a prefix. limit 0 means everything.
func (v *Vercel) listExpanded(ctx context.Context, prefix string, limit int) ([]blobInfo, error) {
	var out []blobInfo
	cursor := ""

	for {
		q := url.Values{}
		q.Set("prefix", prefix)
		q.Set("mode", "expanded")
		if limit > 0 {
			q.Set("limit", fmt.Sprint(limit))
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		v.authorise(req)

		resp, err := v.do(req)
		if err != nil {
			return nil, fmt.Errorf("blob: listing %s: %w", prefix, err)
		}

		var page struct {
			Blobs   []blobInfo `json:"blobs"`
			HasMore bool       `json:"hasMore"`
			Cursor  string     `json:"cursor"`
		}
		if resp.StatusCode != http.StatusOK {
			err := statusError("listing "+prefix, resp)
			_ = resp.Body.Close()
			return nil, err
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("blob: decoding list of %s: %w", prefix, err)
		}

		out = append(out, page.Blobs...)
		if len(page.Blobs) > 0 {
			v.learnHost(page.Blobs[0].URL)
		}
		// Paging matters: a prefix with many objects would silently
		// truncate on the first page otherwise.
		if !page.HasMore || page.Cursor == "" || (limit > 0 && len(out) >= limit) {
			return out, nil
		}
		cursor = page.Cursor
	}
}

// statusError reads the API's error body so a failure says why rather than
// only reporting a status code.
func statusError(what string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var parsed apiError
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		return fmt.Errorf("blob: %s: %s (%s)", what, parsed.Error.Message, parsed.Error.Code)
	}
	return fmt.Errorf("blob: %s: %s: %s", what, resp.Status, strings.TrimSpace(string(body)))
}

// forbiddenError tells an absent object apart from a blocked store.
//
// Both are 403 from a private store. The body is the only signal, so it is
// matched loosely — the exact wording is Vercel's and undocumented, and getting
// this wrong in the safe direction means an outage reported as an outage with
// slightly odd words, while getting it wrong the other way means telling a user
// their data is gone.
func forbiddenError(key string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	text := strings.ToLower(string(body))
	if strings.Contains(text, "blocked") || strings.Contains(text, "suspended") ||
		strings.Contains(text, "quota") {
		return fmt.Errorf("%w: %s", ErrStoreBlocked, strings.TrimSpace(string(body)))
	}
	return ErrNoSuchKey
}
