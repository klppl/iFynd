# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Go service that finds underpriced iPhones, iPads and MacBooks on Tradera by
comparing active fixed-price ("köp nu") listings against historical sold
prices per (model, storage) bucket. Runs on a VPS via docker-compose. No CGO
(modernc.org/sqlite).

## Commands

```sh
go build ./... && go vet ./...   # build + vet
go test ./...                    # all tests
go test ./internal/classify -run TestClassifyRejects -v   # single test
go run . --once                  # one real scrape+compare cycle (hits Tradera)
go run .                         # loop + GUI/API on :8080
docker compose up -d --build     # production
```

For a fast live test without a full backfill:
`IFYND_DB_PATH=/tmp/t.db IFYND_BACKFILL_PAGES=3 go run . --once`

All config is `IFYND_*` env vars with defaults in `loadConfig()` (main.go);
the README documents the most important ones.

## Architecture

Pipeline per run (`App.Run` in app.go): scrape sold → scrape active →
classify → compare → notify. Root package holds the loop, config, and chi
router (server.go); each `internal/` package is one stage.

- **internal/tradera** — fetching + parsing. Tradera category pages are
  Next.js RSC; the full search result is JSON embedded in
  `self.__next_f.push([1,"..."])` script chunks, NOT in the visible HTML.
  `ParseSearchPage` concatenates all chunks, finds the
  `receiveSearchResults` action, and JSON-decodes its `items` array.
  Pagination is `?paging=<page>.a<totalItemCount>.s0` (80 items/page).
  `testdata/sold_page.html` is a real capture — if Tradera changes their
  frontend, re-capture it and fix the parser against it.
- **internal/classify** — (model, storage) bucketing, per family
  (`IPhone`/`IPad`/`MacBook`, selected by the category config). iPhones
  prefer Tradera's structured attributes (`mobile_model`,
  `mobile_disk_memory`, `condition`) with title regexes as fallback. The
  iPad category exposes NO model/storage attributes, so ipad.go normalizes
  titles across generation ("3:e gen"/"7th"/"sjunde generationen"), chip
  (M1–M4, A16, A17 Pro), release year, and Pro/Air screen size into one
  canonical bucket — and skips anything underdetermined (e.g. "iPad Pro
  12.9" alone spans six generations). The laptop category (302393) is
  multi-brand — the macbook family adds `af-computer_brand=Apple` to the
  query via `Category.Filter` — and likewise has no usable attributes, so
  macbook.go buckets Apple Silicon machines as line+screen+chip
  ("MacBook Pro 14 M3 Pro") and Intel-era ones as line+screen+year,
  telling RAM apart from SSD in titles that name both ("16GB/512GB").
  Junk words are family-tuned: "med laddare" is an accessory signal for
  phones but a routine included extra for laptops. Rejects
  accessories/bundles/broken/ambiguous listings with a reason; those land
  in the `skipped_listings` table for auditing. Raw titles are stored
  everywhere so misclassifications can be audited later.
- **internal/store** — SQLite. `sold_listings` is append-only history
  (INSERT OR IGNORE on Tradera id); `active_listings` is upserted with
  `last_seen` refreshed, preserving `first_seen`/`notified`/`broken`.
  Schema changes need a migration in `Open()` (see the `broken` column
  pattern: pragma_table_info check + ALTER TABLE).
- **internal/analyze** — median/trimmed-mean reference price and hit
  threshold math. Pure functions, no I/O.
- **internal/notify** — `Notifier` interface; `log` is the only
  implementation. New channels (ntfy/Discord) get registered in `New()`
  and selected via `IFYND_NOTIFIER`.
- **web/index.html** — the whole GUI, embedded via `go:embed` in server.go.
  Vanilla JS, no external assets (must work offline), Swedish UI copy,
  light+dark via `prefers-color-scheme`.

## Domain invariants (why the code is the way it is)

- Sold items are scraped with `sortBy=AddedOn` because Tradera has **no
  sort-by-end-date**; the default (relevance) returns months-old items
  first. AddedOn-descending bounds incremental depth: anything that sold
  since the last run was *listed* within `IFYND_SOLD_WINDOW_DAYS`.
- For `AuctionBin` items, `price` is the current **bid**; the fixed price
  is `buyNowPrice` (`Item.FixedPrice()`). Sold `price` is the final price.
- Reference prices use median (or trimmed mean), never raw mean — the
  history contains 1 kr auctions and wishful 3× listings.
- A bucket needs ≥ `IFYND_MIN_SAMPLES` sold records or it's ignored
  entirely; a bad guess polluting averages is worse than no signal.
- `is_hit` in the API is purely price-based; the user-set `broken` flag is
  a veto on top (excluded from notifications server-side in `compare`,
  red in the GUI). Both user actions tombstone the id in
  `blocked_listings`: `broken` keeps the row visible but blocks its future
  sold price from the history; `excluded` deletes the row and blocks both
  scrape paths from ever re-adding it. Notifications fire once per listing
  (`notified` flag), retried next run if the notifier errors.
- Scraping is throttled (`Throttle`, delay + jitter) with a real browser
  User-Agent. Keep it polite; don't add parallel fetching.

## Verification expectation

After changes, run the unit tests AND a live `--once` run (see fast-test
command above), then check the run-summary log line (scraped/new/skipped/
buckets/hits counts) for sanity. For GUI changes, screenshot with headless
Chrome against a running server; note this environment forces dark mode, so
verify light mode by stripping the dark media query into a temp copy.
