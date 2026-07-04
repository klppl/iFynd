package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/klppl/ifynd/internal/analyze"
	"github.com/klppl/ifynd/internal/notify"
)

//go:embed web/index.html
var webFS embed.FS

// ListingView is one active listing enriched with its bucket reference for
// the GUI.
type ListingView struct {
	ID        int64     `json:"id"`
	Model     string    `json:"model"`
	StorageGB int       `json:"storage_gb"`
	Price     int       `json:"price"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	FirstSeen time.Time `json:"first_seen"`
	Notified  bool      `json:"notified"`
	Broken    bool      `json:"broken"`
	RefPrice  float64   `json:"ref_price"` // 0 when the bucket has too few samples
	Samples   int       `json:"samples"`
	PctBelow  float64   `json:"pct_below"` // negative = above reference
	IsHit     bool      `json:"is_hit"`
}

// Router exposes the GUI and its JSON API.
func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		page, _ := webFS.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	r.Get("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		st := a.status
		a.mu.Unlock()
		writeJSON(w, st)
	})

	// All active listings with computed bucket references.
	r.Get("/api/listings", func(w http.ResponseWriter, _ *http.Request) {
		views, err := a.listingViews()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, views)
	})

	// Flag or unflag a listing as a broken device.
	r.Post("/api/listings/{id}/broken", func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		var body struct {
			Broken bool `json:"broken"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := a.store.SetBroken(id, body.Broken); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "unknown listing", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"id": id, "broken": body.Broken})
	})

	// Hits found on the most recent run (including previously notified ones).
	r.Get("/api/hits", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		hits := a.hits
		a.mu.Unlock()
		if hits == nil {
			hits = []notify.Hit{}
		}
		writeJSON(w, hits)
	})

	r.Get("/api/buckets", func(w http.ResponseWriter, req *http.Request) {
		since := time.Now().AddDate(0, 0, -a.cfg.LookbackDays)
		buckets, err := a.store.Buckets(since)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, buckets)
	})

	return r
}

// listingViews joins active listings with their bucket reference prices.
func (a *App) listingViews() ([]ListingView, error) {
	actives, err := a.store.ListActive()
	if err != nil {
		return nil, err
	}
	since := time.Now().AddDate(0, 0, -a.cfg.LookbackDays)

	type bucketKey struct {
		model string
		gb    int
	}
	type bucketRef struct {
		ref     float64
		samples int
	}
	refs := map[bucketKey]bucketRef{}

	views := make([]ListingView, 0, len(actives))
	for _, l := range actives {
		key := bucketKey{l.Model, l.StorageGB}
		r, cached := refs[key]
		if !cached {
			prices, err := a.store.SoldPrices(key.model, key.gb, since)
			if err != nil {
				return nil, err
			}
			r.samples = len(prices)
			if r.samples >= a.cfg.MinSamples {
				r.ref = analyze.Reference(prices, a.cfg.Metric, a.cfg.TrimPct)
			}
			refs[key] = r
		}
		v := ListingView{
			ID: l.ID, Model: l.Model, StorageGB: l.StorageGB,
			Price: l.Price, Title: l.Title, URL: l.URL,
			FirstSeen: l.FirstSeen, Notified: l.Notified, Broken: l.Broken,
			Samples: r.samples,
		}
		if r.ref > 0 {
			v.RefPrice = r.ref
			v.PctBelow = analyze.PctBelow(l.Price, r.ref)
			// Purely price-based; the broken flag is the user's veto on top.
			v.IsHit = analyze.IsHit(l.Price, r.ref, a.cfg.ThresholdPct)
		}
		views = append(views, v)
	}
	return views, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
