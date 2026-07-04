// Package notify delivers hit alerts. The channel is pluggable via the
// IFYND_NOTIFIER config; "log" is a stub that writes to the process log.
// To add ntfy/Discord/..., implement Notifier and register it in New.
package notify

import (
	"context"
	"fmt"
	"log/slog"
)

// Hit is an underpriced active listing.
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

func (h Hit) Message() string {
	storage := fmt.Sprintf("%dGB", h.StorageGB)
	if h.StorageGB >= 1024 {
		storage = fmt.Sprintf("%dTB", h.StorageGB/1024)
	}
	return fmt.Sprintf("%s %s för %d kr — %.0f%% under snittet (%.0f kr, %d sålda)\n%s",
		h.Model, storage, h.Price, h.PctBelow, h.RefPrice, h.Samples, h.URL)
}

type Notifier interface {
	Notify(ctx context.Context, hit Hit) error
}

// New returns the notifier named by kind.
func New(kind string) (Notifier, error) {
	switch kind {
	case "", "log":
		return LogNotifier{}, nil
	default:
		return nil, fmt.Errorf("unknown notifier %q (available: log)", kind)
	}
}

// LogNotifier is the stub implementation: it just logs the hit.
type LogNotifier struct{}

func (LogNotifier) Notify(_ context.Context, h Hit) error {
	slog.Info("HIT",
		"model", h.Model, "storage_gb", h.StorageGB,
		"price", h.Price, "ref_price", int(h.RefPrice),
		"pct_below", fmt.Sprintf("%.1f", h.PctBelow),
		"samples", h.Samples, "url", h.URL)
	return nil
}
