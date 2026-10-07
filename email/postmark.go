package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const postmarkURL = "https://api.postmarkapp.com/email"

// Postmark sends through Postmark's API.
type Postmark struct {
	apiKey          string
	from            string
	broadcastStream string
	url             string
	client          *http.Client
}

// NewPostmark builds the sender. A missing key or unusable sender address is an error rather than
// a disabled mailer: a deployment that believes it sends mail and doesn't is noticed only when
// somebody can't reset their password.
func NewPostmark(apiKey, from, broadcastStream string) (*Postmark, error) {
	if apiKey == "" {
		return nil, ErrUnconfigured.With("missing", KeyProviderKey)
	}
	if err := ValidateAddress("from", from); err != nil {
		return nil, ErrUnconfigured.With("missing", KeyFrom)
	}
	return &Postmark{apiKey: apiKey, from: from, broadcastStream: strings.TrimSpace(broadcastStream), url: postmarkURL,
		client: &http.Client{Timeout: 15 * time.Second}}, nil
}

// SetURLForTesting points the sender at a stand-in for Postmark. Nothing outside a test calls it.
func (p *Postmark) SetURLForTesting(url string) { p.url = url }

var (
	_ Sender   = (*Postmark)(nil)
	_ IDSender = (*Postmark)(nil)
)

func (p *Postmark) Send(ctx context.Context, msg Message) error {
	_, err := p.SendWithID(ctx, msg)
	return err
}

func (p *Postmark) Provider() string { return "postmark" }

func (p *Postmark) SendWithID(ctx context.Context, msg Message) (string, error) {
	if err := ValidateAddress("to", msg.To); err != nil {
		return "", err
	}
	if err := ValidateSubject(msg.Subject); err != nil {
		return "", err
	}
	if msg.ReplyTo != "" {
		if err := ValidateAddress("reply_to", msg.ReplyTo); err != nil {
			return "", err
		}
	}
	stream := ""
	if msg.Broadcast {
		if p.broadcastStream == "" {
			return "", ErrNoBroadcastStream
		}
		stream = p.broadcastStream
	}
	body, err := json.Marshal(struct {
		From          string `json:"From"`
		To            string `json:"To"`
		ReplyTo       string `json:"ReplyTo,omitempty"`
		Subject       string `json:"Subject"`
		TextBody      string `json:"TextBody,omitempty"`
		HtmlBody      string `json:"HtmlBody,omitempty"`
		MessageStream string `json:"MessageStream,omitempty"`
	}{p.from, msg.To, msg.ReplyTo, msg.Subject, msg.TextBody, msg.HTMLBody, stream})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Postmark-Server-Token", p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrProviderFailed, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("%w: postmark returned %s: %s", ErrProviderFailed, resp.Status, bytes.TrimSpace(detail))
	}
	var accepted struct {
		MessageID string `json:"MessageID"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&accepted)
	return accepted.MessageID, nil
}
