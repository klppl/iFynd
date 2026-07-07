package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// doJSON posts a JSON body and treats any 2xx as success, surfacing the
// response body on failure so a misconfigured channel is easy to diagnose.
func doJSON(ctx context.Context, url string, payload any, headers map[string]string) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return send(req)
}

// notifyUA is a real browser User-Agent. A notification target behind
// Cloudflare Bot Fight Mode / a WAF will 403 the default "Go-http-client" UA;
// a browser UA is the cheapest thing that often gets a server-to-server call
// through. (If it still 403s, point the channel at the target's internal URL
// to skip the CDN entirely.)
const notifyUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func send(req *http.Request) error {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", notifyUA)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		msg := strings.TrimSpace(string(body))
		// A CDN/proxy in front of the target (e.g. a self-hosted ntfy behind
		// Cloudflare) answers 5xx with an HTML page — report just the status
		// instead of a wall of markup.
		if msg == "" || strings.HasPrefix(msg, "<") {
			return fmt.Errorf("%s", res.Status)
		}
		return fmt.Errorf("%s: %s", res.Status, msg)
	}
	return nil
}

// discord posts to a Discord channel webhook as a plain content message.
type discord struct{ webhook string }

func (d *discord) Notify(ctx context.Context, h Hit) error {
	return doJSON(ctx, d.webhook, map[string]any{
		"content":  "**" + h.Headline() + "**\n" + h.Message(),
		"username": "iFynd",
	}, nil)
}

// ntfy posts to a topic URL as a plain-text body with Title/Click headers.
type ntfy struct {
	topicURL string
	token    string // optional access token
}

func (n *ntfy) Notify(ctx context.Context, h Hit) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.topicURL, strings.NewReader(h.Message()))
	if err != nil {
		return err
	}
	req.Header.Set("Title", asciiHeader(h.Headline()))
	req.Header.Set("Tags", "moneybag")
	if h.URL != "" {
		req.Header.Set("Click", h.URL)
	}
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	return send(req)
}

// gotify posts a message to {server}/message?token={app-token}.
type gotify struct {
	server string
	token  string
}

func (g *gotify) Notify(ctx context.Context, h Hit) error {
	url := strings.TrimRight(g.server, "/") + "/message?token=" + g.token
	return doJSON(ctx, url, map[string]any{
		"title":    h.Headline(),
		"message":  h.Message(),
		"priority": 5,
	}, nil)
}

// webhook posts the full hit as JSON, plus "text"/"title"/"message" convenience
// fields so it also drives Slack/Mattermost-style incoming webhooks.
type webhook struct{ url string }

func (w *webhook) Notify(ctx context.Context, h Hit) error {
	return doJSON(ctx, w.url, map[string]any{
		"text":    h.Headline() + "\n" + h.Message(),
		"title":   h.Headline(),
		"message": h.Message(),
		"hit":     h,
	}, nil)
}

// asciiHeader strips non-ASCII (ntfy's Title header is latin-1 only, so "ä"/"—"
// would otherwise corrupt or be rejected).
func asciiHeader(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)
		} else {
			switch r {
			case 'å', 'ä':
				b.WriteRune('a')
			case 'ö':
				b.WriteRune('o')
			case 'Å', 'Ä':
				b.WriteRune('A')
			case 'Ö':
				b.WriteRune('O')
			case '—', '–':
				b.WriteRune('-')
			}
		}
	}
	return b.String()
}
