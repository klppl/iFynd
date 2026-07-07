package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klppl/ifynd/internal/analyze"
	"github.com/klppl/ifynd/internal/classify"
	"github.com/klppl/ifynd/internal/store"
)

// TestCompareNotifiesWatchlistOnly proves the end-to-end notify path: two
// listings are price-hits, but only the one matching an enabled watchlist alert
// is pushed to the channel, and it's pushed exactly once.
func TestCompareNotifiesWatchlistOnly(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	now := time.Now()
	seed := func(id int64, model string, price int) {
		if _, err := st.InsertSold(store.SoldListing{ID: id, Model: model, StorageGB: 128,
			Price: price, Title: model, SoldAt: now, URL: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	// 6 sold each => median 3000 (iPhone 13) and 4000 (iPhone 15).
	for i := 0; i < 6; i++ {
		seed(int64(100+i), "iPhone 13", 3000)
		seed(int64(200+i), "iPhone 15", 4000)
	}

	var mu sync.Mutex
	var got []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		mu.Lock()
		got = append(got, string(b))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	if _, err := st.UpsertChannel(store.Channel{Kind: "webhook", URL: sink.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Watch iPhone 13 only; iPhone 15 is a hit but must stay silent.
	if _, err := st.UpsertAlert(store.Alert{MatchType: "generation", Pattern: "iPhone 13", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	mk := func(id int64, model string, price int) activeItem {
		l := store.ActiveListing{ID: id, Model: model, StorageGB: 128, Price: price, Title: model, URL: "u"}
		if err := st.UpsertActive(l); err != nil {
			t.Fatal(err)
		}
		return activeItem{listing: l, res: classify.Result{Model: model, StorageGB: 128}}
	}
	actives := []activeItem{
		mk(101, "iPhone 13", 2000), // 33% below 3000 => hit + watched
		mk(201, "iPhone 15", 3000), // 25% below 4000 => hit, not watched
	}

	app := &App{cfg: Config{LookbackDays: 90, Metric: analyze.Median, TrimPct: 10}, store: st}
	eff := Tuning{ThresholdPct: 15, MinSamples: 5, MinPrice: 100}

	var status Status
	if err := app.compare(context.Background(), actives, &status, eff); err != nil {
		t.Fatal(err)
	}
	if status.HitsLastRun != 2 {
		t.Errorf("radar hits = %d, want 2 (both underpriced)", status.HitsLastRun)
	}
	if status.NotifiedLastRun != 1 {
		t.Errorf("notified = %d, want 1 (watchlist-only)", status.NotifiedLastRun)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], "iPhone 13") || strings.Contains(got[0], "iPhone 15") {
		t.Fatalf("sink got %v, want exactly one iPhone 13 notification", got)
	}

	// Second run: already notified => no repeat push.
	var status2 Status
	if err := app.compare(context.Background(), actives, &status2, eff); err != nil {
		t.Fatal(err)
	}
	if status2.NotifiedLastRun != 0 {
		t.Errorf("second run notified = %d, want 0 (notified flag)", status2.NotifiedLastRun)
	}
}

func TestMatchAlert(t *testing.T) {
	// ref 10000; global threshold 15% => hit under 8500.
	const ref = 10000.0
	const global = 15.0

	rules := []store.Alert{
		{ID: 1, MatchType: "model", Pattern: "iPhone 16 Pro", Enabled: true},
		{ID: 2, MatchType: "generation", Pattern: "iPhone 15", MaxPrice: 5000, Enabled: true},
		{ID: 3, MatchType: "model", Pattern: "iPhone 14", StorageGB: 256, Enabled: true},
		{ID: 4, MatchType: "model", Pattern: "iPhone 13", MinPctBelow: 5, Enabled: true},
		{ID: 5, MatchType: "model", Pattern: "iPhone 12", Enabled: false},
	}

	tests := []struct {
		name       string
		model      string
		gb, price  int
		wantRuleID int64 // 0 = no match
	}{
		{"exact model underpriced", "iPhone 16 Pro", 256, 8000, 1},
		{"exact model not cheap enough", "iPhone 16 Pro", 256, 9000, 0},
		{"generation match under ceiling", "iPhone 15 Pro", 128, 4500, 2},
		{"generation match over ceiling", "iPhone 15 Pro", 128, 6000, 0},
		{"storage-specific match", "iPhone 14", 256, 8000, 3},
		{"storage-specific wrong storage", "iPhone 14", 128, 8000, 0},
		{"per-rule looser threshold fires", "iPhone 13", 128, 9400, 4}, // 6% below, global 15% wouldn't
		{"disabled rule never fires", "iPhone 12", 128, 1000, 0},
		{"unwatched model", "iPhone 11", 128, 1000, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchAlert(rules, tt.model, tt.gb, tt.price, ref, global)
			var id int64
			if got != nil {
				id = got.ID
			}
			if id != tt.wantRuleID {
				t.Errorf("matchAlert(%s, %d, %d) = rule %d, want %d", tt.model, tt.gb, tt.price, id, tt.wantRuleID)
			}
		})
	}
}
