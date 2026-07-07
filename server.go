package main

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/klppl/ifynd/internal/analyze"
	"github.com/klppl/ifynd/internal/notify"
	"github.com/klppl/ifynd/internal/store"
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
	Title     string     `json:"title"`
	URL       string     `json:"url"`
	FirstSeen time.Time  `json:"first_seen"`
	ListedAt  *time.Time `json:"listed_at,omitempty"` // nil on pre-migration rows
	Notified  bool       `json:"notified"`
	Broken    bool      `json:"broken"`
	RefPrice  float64   `json:"ref_price"` // 0 when the bucket has too few samples
	Samples   int       `json:"samples"`
	PctBelow  float64   `json:"pct_below"` // negative = above reference
	IsHit     bool      `json:"is_hit"`
	Alerted   bool      `json:"alerted"` // matches an enabled watchlist rule
}

const sessionCookie = "ifynd_session"

// sessions is the in-memory set of logged-in tokens. A restart logs
// everyone out, which is fine for a single-user tool.
type sessions struct {
	mu     sync.Mutex
	tokens map[string]struct{}
}

func (s *sessions) new() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	s.mu.Lock()
	s.tokens[tok] = struct{}{}
	s.mu.Unlock()
	return tok, nil
}

func (s *sessions) valid(req *http.Request) bool {
	c, err := req.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.mu.Lock()
	_, ok := s.tokens[c.Value]
	s.mu.Unlock()
	return ok
}

// Router exposes the GUI and its JSON API.
func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	sess := &sessions{tokens: map[string]struct{}{}}

	// requireAuth guards mutating endpoints when the GUI is hosted
	// publicly (IFYND_PUBLIC). Reading stays open; writes need a login.
	requireAuth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			if a.cfg.Public && !sess.valid(req) {
				http.Error(w, "login required", http.StatusUnauthorized)
				return
			}
			next(w, req)
		}
	}

	r.Get("/api/auth", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, map[string]bool{
			"public":        a.cfg.Public,
			"authenticated": !a.cfg.Public || sess.valid(req),
		})
	})

	r.Post("/api/login", func(w http.ResponseWriter, req *http.Request) {
		if a.cfg.WebPassword == "" {
			http.Error(w, "login disabled", http.StatusNotFound)
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.cfg.WebPassword)) != 1 {
			time.Sleep(time.Second) // slow down guessing
			http.Error(w, "wrong password", http.StatusUnauthorized)
			return
		}
		tok, err := sess.new()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    tok,
			Path:     "/",
			MaxAge:   30 * 24 * 60 * 60,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		writeJSON(w, map[string]bool{"ok": true})
	})

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
	r.Post("/api/listings/{id}/broken", requireAuth(func(w http.ResponseWriter, req *http.Request) {
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
	}))

	// Remove a listing permanently: deleted from the active table and
	// tombstoned so future scrapes never re-add it.
	r.Post("/api/listings/{id}/exclude", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := a.store.Exclude(id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "unknown listing", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"id": id, "excluded": true})
	}))

	// Historical sales that were bargains: sold below the bucket reference
	// by at least the hit threshold.
	r.Get("/api/bargains", func(w http.ResponseWriter, _ *http.Request) {
		views, err := a.bargainViews()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, views)
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

	a.adminRoutes(r, requireAuth)

	return r
}

// adminRoutes registers the admin GUI's API: detection tuning, notification
// channels and the watchlist. Everything is behind requireAuth (which only
// blocks when the GUI is public), because it reads/writes channel secrets.
func (a *App) adminRoutes(r chi.Router, requireAuth func(http.HandlerFunc) http.HandlerFunc) {
	// Detection tuning: effective values (env defaults + admin overrides).
	r.Get("/api/admin/config", requireAuth(func(w http.ResponseWriter, _ *http.Request) {
		eff := a.tuning()
		writeJSON(w, map[string]any{
			"threshold_pct": eff.ThresholdPct,
			"min_samples":   eff.MinSamples,
			"min_price":     eff.MinPrice,
			// read-only context, configured via env
			"interval":      a.cfg.Interval.String(),
			"metric":        string(a.cfg.Metric),
			"lookback_days": a.cfg.LookbackDays,
		})
	}))

	r.Post("/api/admin/config", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		var b struct {
			ThresholdPct *float64 `json:"threshold_pct"`
			MinSamples   *int     `json:"min_samples"`
			MinPrice     *int     `json:"min_price"`
		}
		if err := json.NewDecoder(req.Body).Decode(&b); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if b.ThresholdPct != nil {
			if *b.ThresholdPct <= 0 || *b.ThresholdPct >= 100 {
				http.Error(w, "threshold_pct must be between 0 and 100", http.StatusBadRequest)
				return
			}
			a.store.SetSetting("threshold_pct", strconv.FormatFloat(*b.ThresholdPct, 'f', -1, 64))
		}
		if b.MinSamples != nil {
			if *b.MinSamples < 1 {
				http.Error(w, "min_samples must be >= 1", http.StatusBadRequest)
				return
			}
			a.store.SetSetting("min_samples", strconv.Itoa(*b.MinSamples))
		}
		if b.MinPrice != nil {
			if *b.MinPrice < 0 {
				http.Error(w, "min_price must be >= 0", http.StatusBadRequest)
				return
			}
			a.store.SetSetting("min_price", strconv.Itoa(*b.MinPrice))
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))

	// Notification channels.
	r.Get("/api/admin/channels", requireAuth(func(w http.ResponseWriter, _ *http.Request) {
		channels, err := a.store.Channels()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if channels == nil {
			channels = []store.Channel{}
		}
		writeJSON(w, channels)
	}))

	r.Post("/api/admin/channels", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		var c store.Channel
		if err := json.NewDecoder(req.Body).Decode(&c); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if _, err := notify.Build(c.Kind, c.URL, c.Token); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := a.store.UpsertChannel(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		c.ID = id
		writeJSON(w, c)
	}))

	r.Post("/api/admin/channels/{id}/delete", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := a.store.DeleteChannel(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))

	// Send a sample notification to a channel config (tested before/without
	// saving) so the user can confirm their webhook works.
	r.Post("/api/admin/channels/test", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		var c store.Channel
		if err := json.NewDecoder(req.Body).Decode(&c); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		n, err := notify.Build(c.Kind, c.URL, c.Token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sample := notify.Hit{
			Model: "iPhone 16 Pro", StorageGB: 256, Price: 6999,
			RefPrice: 8500, PctBelow: 17.7, Samples: 42,
			Title: "Testnotis från iFynd", URL: "https://www.tradera.com",
		}
		// A failed delivery is an expected outcome (bad URL/token), not a
		// server error: report it as 200 {ok:false,error} so a proxy/CDN in
		// front (e.g. Cloudflare) doesn't swallow a 5xx and replace the body
		// with its own error page.
		if err := n.Notify(req.Context(), sample); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))

	// Watchlist alerts.
	r.Get("/api/admin/alerts", requireAuth(func(w http.ResponseWriter, _ *http.Request) {
		alerts, err := a.store.Alerts()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if alerts == nil {
			alerts = []store.Alert{}
		}
		writeJSON(w, alerts)
	}))

	r.Post("/api/admin/alerts", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		var al store.Alert
		if err := json.NewDecoder(req.Body).Decode(&al); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if al.MatchType != "model" && al.MatchType != "generation" {
			http.Error(w, "match_type must be model or generation", http.StatusBadRequest)
			return
		}
		if al.Pattern == "" {
			http.Error(w, "pattern required", http.StatusBadRequest)
			return
		}
		id, err := a.store.UpsertAlert(al)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		al.ID = id
		writeJSON(w, al)
	}))

	r.Post("/api/admin/alerts/{id}/delete", requireAuth(func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		if err := a.store.DeleteAlert(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
}

// refCache lazily computes the reference price per (model, storage) bucket
// over the configured lookback window.
type bucketKey struct {
	model string
	gb    int
}

type bucketRef struct {
	ref     float64
	samples int
}

type refCache struct {
	a          *App
	since      time.Time
	minSamples int
	refs       map[bucketKey]bucketRef
}

func (a *App) newRefCache(minSamples int) *refCache {
	return &refCache{
		a:          a,
		since:      time.Now().AddDate(0, 0, -a.cfg.LookbackDays),
		minSamples: minSamples,
		refs:       map[bucketKey]bucketRef{},
	}
}

func (c *refCache) get(model string, gb int) (ref float64, samples int, err error) {
	key := bucketKey{model, gb}
	r, ok := c.refs[key]
	if !ok {
		prices, err := c.a.store.SoldPrices(model, gb, c.since)
		if err != nil {
			return 0, 0, err
		}
		r.samples = len(prices)
		if r.samples >= c.minSamples {
			r.ref = analyze.Reference(prices, c.a.cfg.Metric, c.a.cfg.TrimPct)
		}
		c.refs[key] = r
	}
	return r.ref, r.samples, nil
}

// listingViews joins active listings with their bucket reference prices.
func (a *App) listingViews() ([]ListingView, error) {
	actives, err := a.store.ListActive()
	if err != nil {
		return nil, err
	}
	eff := a.tuning()
	alerts, err := a.store.Alerts()
	if err != nil {
		return nil, err
	}
	cache := a.newRefCache(eff.MinSamples)
	views := make([]ListingView, 0, len(actives))
	for _, l := range actives {
		ref, samples, err := cache.get(l.Model, l.StorageGB)
		if err != nil {
			return nil, err
		}
		v := ListingView{
			ID: l.ID, Model: l.Model, StorageGB: l.StorageGB,
			Price: l.Price, Title: l.Title, URL: l.URL,
			FirstSeen: l.FirstSeen, Notified: l.Notified, Broken: l.Broken,
			Samples: samples,
		}
		if !l.ListedAt.IsZero() {
			t := l.ListedAt
			v.ListedAt = &t
		}
		if ref > 0 {
			v.RefPrice = ref
			v.PctBelow = analyze.PctBelow(l.Price, ref)
			// Purely price-based; the broken flag is the user's veto on top.
			v.IsHit = analyze.IsHit(l.Price, ref, eff.ThresholdPct)
			v.Alerted = !l.Broken && matchAlert(alerts, l.Model, l.StorageGB, l.Price, ref, eff.ThresholdPct) != nil
		}
		views = append(views, v)
	}
	return views, nil
}

// BargainView is one historical sale that went for a good price.
type BargainView struct {
	ID         int64      `json:"id"`
	Model      string     `json:"model"`
	StorageGB  int        `json:"storage_gb"`
	Price      int        `json:"price"`
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	SoldAt     time.Time  `json:"sold_at"`
	ListedAt   *time.Time `json:"listed_at,omitempty"`
	DaysListed *int       `json:"days_listed,omitempty"` // nil when listed_at is unknown
	RefPrice   float64    `json:"ref_price"`
	Samples    int        `json:"samples"`
	PctBelow   float64    `json:"pct_below"`
}

// bargainViews returns sold listings within the lookback window whose final
// price undercut their bucket reference by at least the hit threshold.
func (a *App) bargainViews() ([]BargainView, error) {
	eff := a.tuning()
	cache := a.newRefCache(eff.MinSamples)
	sold, err := a.store.ListSold(cache.since)
	if err != nil {
		return nil, err
	}
	views := []BargainView{}
	for _, l := range sold {
		ref, samples, err := cache.get(l.Model, l.StorageGB)
		if err != nil {
			return nil, err
		}
		if !analyze.IsHit(l.Price, ref, eff.ThresholdPct) {
			continue
		}
		v := BargainView{
			ID: l.ID, Model: l.Model, StorageGB: l.StorageGB,
			Price: l.Price, Title: l.Title, URL: l.URL,
			SoldAt: l.SoldAt, RefPrice: ref, Samples: samples,
			PctBelow: analyze.PctBelow(l.Price, ref),
		}
		if !l.ListedAt.IsZero() {
			t := l.ListedAt
			v.ListedAt = &t
			days := int(math.Round(l.SoldAt.Sub(l.ListedAt).Hours() / 24))
			v.DaysListed = &days
		}
		views = append(views, v)
	}
	return views, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
