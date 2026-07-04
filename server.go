package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/klppl/ifynd/internal/notify"
)

// Router exposes read-only inspection endpoints.
func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})

	r.Get("/api/status", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		st := a.status
		a.mu.Unlock()
		writeJSON(w, st)
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
