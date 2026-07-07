// Package notify delivers hit alerts to one or more channels. Channels are
// configured at runtime (stored in the DB, edited from the admin GUI):
// Discord, ntfy, Gotify and generic webhooks, plus a "log" channel that always
// writes to the process log. Build turns a stored config into a Notifier;
// Multi fans a hit out to several.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Hit is an underpriced active listing worth notifying about.
type Hit struct {
	ListingID int64   `json:"listing_id"`
	Model     string  `json:"model"`
	StorageGB int     `json:"storage_gb"`
	Price     int     `json:"price"`     // SEK
	RefPrice  float64 `json:"ref_price"` // historical median/trimmed mean, SEK
	PctBelow  float64 `json:"pct_below"`
	Samples   int     `json:"samples"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
}

func storageText(gb int) string {
	if gb >= 1024 {
		return fmt.Sprintf("%dTB", gb/1024)
	}
	return fmt.Sprintf("%dGB", gb)
}

// Headline is a one-line title for push channels.
func (h Hit) Headline() string {
	return fmt.Sprintf("Fynd: %s %s för %d kr", h.Model, storageText(h.StorageGB), h.Price)
}

// Message is the full human-readable body, URL included.
func (h Hit) Message() string {
	return fmt.Sprintf("%s %s för %d kr — %.0f%% under snittet (%.0f kr, %d sålda)\n%s",
		h.Model, storageText(h.StorageGB), h.Price, h.PctBelow, h.RefPrice, h.Samples, h.URL)
}

type Notifier interface {
	Notify(ctx context.Context, hit Hit) error
}

// httpClient is shared by all HTTP channels; a hit notification should never
// hang the run loop.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// Build constructs a channel from its stored kind/url/token. The "log" kind is
// accepted so tests and defaults have a no-network target.
func Build(kind, url, token string) (Notifier, error) {
	url = strings.TrimSpace(url)
	// Trim the token and drop an accidentally-pasted scheme prefix, so a
	// value copied as "Bearer tk_…" or "tk_… " still authenticates.
	token = strings.TrimSpace(token)
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	switch kind {
	case "", "log":
		return LogNotifier{}, nil
	case "discord":
		if url == "" {
			return nil, fmt.Errorf("discord: webhook-URL krävs")
		}
		return &discord{webhook: url}, nil
	case "ntfy":
		if url == "" {
			return nil, fmt.Errorf("ntfy: topic-URL krävs (t.ex. https://ntfy.sh/mitt-ämne)")
		}
		return &ntfy{topicURL: url, token: token}, nil
	case "gotify":
		if url == "" || token == "" {
			return nil, fmt.Errorf("gotify: server-URL och app-token krävs")
		}
		return &gotify{server: url, token: token}, nil
	case "webhook":
		if url == "" {
			return nil, fmt.Errorf("webhook: URL krävs")
		}
		return &webhook{url: url}, nil
	default:
		return nil, fmt.Errorf("okänd kanaltyp %q (vill ha discord, ntfy, gotify eller webhook)", kind)
	}
}

// Multi fans a hit out to every channel, joining their errors so one bad
// channel doesn't stop the others.
type Multi []Notifier

func (m Multi) Notify(ctx context.Context, h Hit) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, h); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LogNotifier writes the hit to the process log; always part of Multi.
type LogNotifier struct{}

func (LogNotifier) Notify(_ context.Context, h Hit) error {
	slog.Info("ALERT",
		"model", h.Model, "storage_gb", h.StorageGB,
		"price", h.Price, "ref_price", int(h.RefPrice),
		"pct_below", fmt.Sprintf("%.1f", h.PctBelow),
		"samples", h.Samples, "url", h.URL)
	return nil
}
