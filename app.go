package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/klppl/ifynd/internal/analyze"
	"github.com/klppl/ifynd/internal/classify"
	"github.com/klppl/ifynd/internal/notify"
	"github.com/klppl/ifynd/internal/store"
	"github.com/klppl/ifynd/internal/tradera"
)

type App struct {
	cfg      Config
	store    *store.Store
	client   *tradera.Client
	notifier notify.Notifier

	mu     sync.Mutex
	status Status
	hits   []notify.Hit // hits found on the last run (for /api/hits)
}

type Status struct {
	LastRunStart    time.Time `json:"last_run_start"`
	LastRunDuration string    `json:"last_run_duration"`
	LastRunError    string    `json:"last_run_error,omitempty"`
	SoldScraped     int       `json:"sold_scraped_last_run"`
	SoldNew         int       `json:"sold_new_last_run"`
	SoldTotal       int       `json:"sold_total"`
	ActiveScraped   int       `json:"active_scraped_last_run"`
	SkippedLastRun  int       `json:"skipped_last_run"`
	BucketsChecked  int       `json:"buckets_checked_last_run"`
	HitsLastRun     int       `json:"hits_last_run"`
	NotifiedLastRun int       `json:"notified_last_run"`
	RunsCompleted   int       `json:"runs_completed"`
}

// Run performs one scrape + compare cycle.
func (a *App) Run(ctx context.Context) error {
	start := time.Now()
	slog.Info("run starting")

	var st Status
	st.LastRunStart = start.UTC()

	runErr := func() error {
		if err := a.scrapeSold(ctx, &st); err != nil {
			return fmt.Errorf("scrape sold: %w", err)
		}
		actives, err := a.scrapeActive(ctx, &st)
		if err != nil {
			return fmt.Errorf("scrape active: %w", err)
		}
		if err := a.compare(ctx, actives, &st); err != nil {
			return fmt.Errorf("compare: %w", err)
		}
		if pruned, err := a.store.PruneActive(time.Now().Add(-72 * time.Hour)); err != nil {
			slog.Warn("prune active", "err", err)
		} else if pruned > 0 {
			slog.Info("pruned stale active listings", "count", pruned)
		}
		return nil
	}()

	if total, err := a.store.SoldCount(); err == nil {
		st.SoldTotal = total
	}
	st.LastRunDuration = time.Since(start).Round(time.Millisecond).String()
	if runErr != nil {
		st.LastRunError = runErr.Error()
	}

	a.mu.Lock()
	st.RunsCompleted = a.status.RunsCompleted + 1
	a.status = st
	a.mu.Unlock()

	slog.Info("run finished", "duration", st.LastRunDuration,
		"sold_scraped", st.SoldScraped, "sold_new", st.SoldNew,
		"active_scraped", st.ActiveScraped, "skipped", st.SkippedLastRun,
		"buckets", st.BucketsChecked, "hits", st.HitsLastRun, "notified", st.NotifiedLastRun)
	return runErr
}

// scrapeSold walks the sold list (newest listings first) and appends new
// records. On an empty DB it backfills up to BackfillPages; otherwise it stops
// once a page contains only listings started before the SoldWindowDays cutoff
// — everything that can have sold since the last run was listed after it.
func (a *App) scrapeSold(ctx context.Context, st *Status) error {
	existing, err := a.store.SoldCount()
	if err != nil {
		return err
	}
	maxPages := a.cfg.SoldMaxPages
	cutoff := time.Now().AddDate(0, 0, -a.cfg.SoldWindowDays)
	if existing == 0 {
		maxPages = a.cfg.BackfillPages
		cutoff = time.Time{} // take all history Tradera still serves
		slog.Info("empty database, backfilling sold history", "max_pages", maxPages)
	}

	q := tradera.SoldQuery(a.cfg.CategoryID)
	total := 0
	for page := 1; page <= maxPages; page++ {
		if page > 1 {
			if err := a.client.Throttle(ctx); err != nil {
				return err
			}
		}
		res, err := a.client.FetchPage(ctx, q, page, total)
		if err != nil {
			return err
		}
		total = res.TotalItemCount
		if len(res.Items) == 0 {
			break
		}

		pageOldest := time.Now()
		for i := range res.Items {
			it := &res.Items[i]
			st.SoldScraped++
			if it.StartDate.Before(pageOldest) {
				pageOldest = it.StartDate
			}
			c, ok, reason := classify.Item(it)
			if !ok {
				st.SkippedLastRun++
				a.store.RecordSkipped(it.ItemID, "sold", it.ShortDescription, it.ItemURL, reason)
				continue
			}
			inserted, err := a.store.InsertSold(store.SoldListing{
				ID: it.ItemID, Model: c.Model, StorageGB: c.StorageGB,
				Price: it.Price, Title: it.ShortDescription,
				SoldAt: it.EndDate, URL: it.ItemURL,
			})
			if err != nil {
				return err
			}
			if inserted {
				st.SoldNew++
			}
		}

		if page >= res.PageCount {
			break
		}
		if !cutoff.IsZero() && pageOldest.Before(cutoff) {
			break
		}
	}
	return nil
}

type activeItem struct {
	listing store.ActiveListing
	res     classify.Result
}

// scrapeActive walks all active fixed-price pages and upserts classified
// listings. It returns this run's classified items so the comparison never
// considers listings that have already ended.
func (a *App) scrapeActive(ctx context.Context, st *Status) ([]activeItem, error) {
	q := tradera.ActiveQuery(a.cfg.CategoryID)
	var out []activeItem
	total := 0
	for page := 1; page <= a.cfg.ActiveMaxPages; page++ {
		if err := a.client.Throttle(ctx); err != nil {
			return nil, err
		}
		res, err := a.client.FetchPage(ctx, q, page, total)
		if err != nil {
			return nil, err
		}
		total = res.TotalItemCount
		if len(res.Items) == 0 {
			break
		}
		for i := range res.Items {
			it := &res.Items[i]
			st.ActiveScraped++
			c, ok, reason := classify.Item(it)
			if !ok {
				st.SkippedLastRun++
				a.store.RecordSkipped(it.ItemID, "active", it.ShortDescription, it.ItemURL, reason)
				continue
			}
			l := store.ActiveListing{
				ID: it.ItemID, Model: c.Model, StorageGB: c.StorageGB,
				Price: it.FixedPrice(), Title: it.ShortDescription, URL: it.ItemURL,
			}
			if err := a.store.UpsertActive(l); err != nil {
				return nil, err
			}
			out = append(out, activeItem{listing: l, res: c})
		}
		if page >= res.PageCount {
			break
		}
	}
	return out, nil
}

// compare checks every classified active listing against its bucket's
// reference price and notifies new hits.
func (a *App) compare(ctx context.Context, actives []activeItem, st *Status) error {
	since := time.Now().AddDate(0, 0, -a.cfg.LookbackDays)

	type bucketKey struct {
		model string
		gb    int
	}
	refs := map[bucketKey]struct {
		ref     float64
		samples int
	}{}

	var hits []notify.Hit
	for _, ai := range actives {
		key := bucketKey{ai.res.Model, ai.res.StorageGB}
		r, cached := refs[key]
		if !cached {
			prices, err := a.store.SoldPrices(key.model, key.gb, since)
			if err != nil {
				return err
			}
			r.samples = len(prices)
			if r.samples >= a.cfg.MinSamples {
				r.ref = analyze.Reference(prices, a.cfg.Metric, a.cfg.TrimPct)
			}
			refs[key] = r
		}
		if r.samples < a.cfg.MinSamples || r.ref <= 0 {
			continue
		}
		if !analyze.IsHit(ai.listing.Price, r.ref, a.cfg.ThresholdPct) {
			continue
		}
		// User-flagged broken devices are false positives, not deals.
		if _, broken, err := a.store.Flags(ai.listing.ID); err != nil {
			return err
		} else if broken {
			continue
		}
		hits = append(hits, notify.Hit{
			ListingID: ai.listing.ID,
			Model:     ai.res.Model, StorageGB: ai.res.StorageGB,
			Price: ai.listing.Price, RefPrice: r.ref,
			PctBelow: analyze.PctBelow(ai.listing.Price, r.ref),
			Samples:  r.samples,
			Title:    ai.listing.Title, URL: ai.listing.URL,
		})
	}
	st.BucketsChecked = len(refs)
	st.HitsLastRun = len(hits)

	var errs []error
	for _, h := range hits {
		notified, _, err := a.store.Flags(h.ListingID)
		if err != nil {
			return err
		}
		if notified {
			continue
		}
		if err := a.notifier.Notify(ctx, h); err != nil {
			// Leave notified=0 so the next run retries.
			errs = append(errs, fmt.Errorf("notify %d: %w", h.ListingID, err))
			continue
		}
		if err := a.store.MarkNotified(h.ListingID); err != nil {
			return err
		}
		st.NotifiedLastRun++
	}

	a.mu.Lock()
	a.hits = hits
	a.mu.Unlock()
	return errors.Join(errs...)
}
