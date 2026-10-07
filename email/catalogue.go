package email

import (
	"embed"
	"encoding/json"
	"html"
	"io/fs"
	"net/url"
	"regexp"
	"strings"

	"github.com/nickwhiteley/plinth/code"
)

// BaseLocale is the locale every message exists in, and the fallback for any other.
const BaseLocale = "en-GB"

//go:embed messages/*.json
var plinthMessages embed.FS

// Messages is plinth's own message files (en-GB): the subjects and bodies of its kinds. A product
// loads them first and its own over them, so it may override any.
func Messages() fs.FS { sub, _ := fs.Sub(plinthMessages, "messages"); return sub }

var (
	// ErrMessageMissing is a message id with no text in the base locale. It carries "id".
	ErrMessageMissing = code.New("email.message_missing")
	// ErrParamMissing is a {param} a message uses that wasn't supplied. It carries "id" and "param".
	ErrParamMissing = code.New("email.param_missing")
	// ErrLinkInvalid is a "link" parameter that isn't an absolute http or https URL: escaping
	// stops a link breaking out of its attribute, but not a javascript: link working.
	ErrLinkInvalid = code.New("email.link_invalid")
	// ErrCatalogue is a message file that isn't a flat JSON object of strings. It carries "file".
	ErrCatalogue = code.New("email.catalogue_invalid")
)

// Catalogue is message text by locale and id: the flat JSON files (<locale>.json, {"id": "text
// with {param}"}) a product's web app reads too, so there is one set of ids and one translation
// workflow (Furniture Magic spec §13).
type Catalogue struct{ text map[string]map[string]string }

var localeFile = regexp.MustCompile(`^([a-z]{2,3}(-[A-Z]{2,3})?)\.json$`)

// LoadCatalogue reads message files from each fs in turn; a later file's id overrides an earlier
// one's. Files that aren't <locale>.json are ignored, and a key starting with "$" (Paraglide's
// $schema) is skipped.
func LoadCatalogue(sources ...fs.FS) (*Catalogue, error) {
	c := &Catalogue{text: map[string]map[string]string{}}
	for _, src := range sources {
		entries, err := fs.ReadDir(src, ".")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			m := localeFile.FindStringSubmatch(e.Name())
			if m == nil || e.IsDir() {
				continue
			}
			b, err := fs.ReadFile(src, e.Name())
			if err != nil {
				return nil, err
			}
			var raw map[string]any
			if err := json.Unmarshal(b, &raw); err != nil {
				return nil, ErrCatalogue.With("file", e.Name())
			}
			if c.text[m[1]] == nil {
				c.text[m[1]] = map[string]string{}
			}
			for k, v := range raw {
				if strings.HasPrefix(k, "$") {
					continue
				}
				s, ok := v.(string)
				if !ok {
					return nil, ErrCatalogue.With("file", e.Name())
				}
				c.text[m[1]][k] = s
			}
		}
	}
	return c, nil
}

var param = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// raw returns a message's text in a locale, falling back to the base locale.
func (c *Catalogue) raw(locale, id string) (string, error) {
	if t, ok := c.text[locale][id]; ok {
		return t, nil
	}
	if t, ok := c.text[BaseLocale][id]; ok {
		return t, nil
	}
	return "", ErrMessageMissing.With("id", id)
}

// fill replaces each {param} with fn(name), and reports the first one params lacks.
func fill(id, t string, params map[string]string, fn func(string) string) (string, error) {
	var missing string
	out := param.ReplaceAllStringFunc(t, func(m string) string {
		name := m[1 : len(m)-1]
		if _, ok := params[name]; !ok && missing == "" {
			missing = name
		}
		return fn(name)
	})
	if missing != "" {
		return "", ErrParamMissing.With("id", id).With("param", missing)
	}
	return out, nil
}

// Text returns a message in a locale, falling back to the base locale, with its {params} filled in.
func (c *Catalogue) Text(locale, id string, params map[string]string) (string, error) {
	t, err := c.raw(locale, id)
	if err != nil {
		return "", err
	}
	return fill(id, t, params, func(name string) string { return params[name] })
}

// Missing returns the ids the base locale has and the given locale lacks: the check that fails a
// build when a translation falls behind.
func (c *Catalogue) Missing(locale string) []string {
	var out []string
	for id := range c.text[BaseLocale] {
		if _, ok := c.text[locale][id]; !ok {
			out = append(out, id)
		}
	}
	return out
}

// Rendered is a message's subject and bodies.
type Rendered struct {
	Subject  string
	TextBody string
	HTMLBody string
}

// Render renders a kind's email: "email.<kind>.subject" and "email.<kind>.body", whose blank lines
// separate paragraphs. The HTML body escapes every parameter, and turns the "link" parameter, when
// given, into the link it is.
func (c *Catalogue) Render(locale string, k Kind, params map[string]string) (Rendered, error) {
	id := "email." + string(k)
	if link, ok := params["link"]; ok {
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Rendered{}, ErrLinkInvalid
		}
	}
	subject, err := c.Text(locale, id+".subject", params)
	if err != nil {
		return Rendered{}, err
	}
	text, err := c.Text(locale, id+".body", params)
	if err != nil {
		return Rendered{}, err
	}
	// HTML: escape the message's own text a paragraph at a time, then put in parameters escaped
	// on their own (html.EscapeString leaves the braces alone). The link becomes a link.
	tmpl, err := c.raw(locale, id+".body")
	if err != nil {
		return Rendered{}, err
	}
	var b strings.Builder
	for _, para := range strings.Split(tmpl, "\n\n") {
		p, err := fill(id+".body", html.EscapeString(strings.TrimSpace(para)), params, func(name string) string {
			v := html.EscapeString(params[name])
			if name == "link" {
				return `<a href="` + v + `">` + v + `</a>`
			}
			return v
		})
		if err != nil {
			return Rendered{}, err
		}
		if p != "" {
			b.WriteString("<p>" + strings.ReplaceAll(p, "\n", "<br>") + "</p>\n")
		}
	}
	return Rendered{Subject: strings.TrimSpace(subject), TextBody: text, HTMLBody: b.String()}, nil
}
