# iFynd

Finds underpriced iPhones on [Tradera](https://www.tradera.com) by comparing
active fixed-price ("köp nu") listings against historical sold prices, per
(model, storage) bucket.

## How it works

Tradera category pages are Next.js RSC pages that embed the complete search
result as JSON in `self.__next_f.push()` script chunks — including structured
attributes (`mobile_model`, `mobile_disk_memory`, `condition`) that are more
reliable than the free-text titles. iFynd:

1. Scrapes sold listings (`itemStatus=Sold&sortBy=AddedOn`, category 340186)
   into an append-only price history. First run backfills everything Tradera
   still serves (~90 days); later runs only walk pages until listings are
   older than `IFYND_SOLD_WINDOW_DAYS`.
2. Scrapes active fixed-price listings and upserts them (dedup on Tradera
   listing id, `last_seen` refreshed each run).
3. Classifies each listing into a (model, storage) bucket — structured
   attributes first, title parsing as fallback. Accessories, bundles,
   broken/parts phones and ambiguous titles are skipped and logged to
   `skipped_listings` for auditing.
4. For each bucket with ≥ `IFYND_MIN_SAMPLES` sold records in the lookback
   window, computes a reference price (median by default, or trimmed mean)
   and flags active listings more than `IFYND_THRESHOLD_PCT` below it.
5. Notifies once per listing (`notified` flag). The notifier is an interface;
   `log` is the built-in stub — add ntfy/Discord in `internal/notify`.

## Run

```sh
go run . --once        # single scrape+compare cycle
go run .               # loop every IFYND_INTERVAL (default 30m) + HTTP API
docker compose up -d   # on the VPS; SQLite persisted in the ifynd-data volume
```

## Web GUI

`http://<host>:8080/` serves a single-page dashboard (embedded in the binary)
with two tabs:

- **Aktiva annonser** — all active listings with price, bucket median, a
  diverging price-gap bar, and sample count. Hits are highlighted; filters
  for search/model/only-hits. Two actions per row:
  - **Trasig** marks a broken device (red) — excluded from hits and
    notifications, and its price never enters the sold history if it sells
    (undo with **Ångra**).
  - **Exkludera** deletes the listing and tombstones its id so no future
    scrape re-adds it.
- **Sålda fynd** — historical sales that went below the bucket reference by
  at least the hit threshold, including how many days each was listed before
  it sold (blank for records scraped before listing dates were stored).

## HTTP API

- `GET /healthz`
- `GET /api/status` — last run stats
- `GET /api/listings` — active listings with computed references and flags
- `POST /api/listings/{id}/broken` — body `{"broken": true|false}`
- `POST /api/listings/{id}/exclude` — delete + tombstone a listing
- `GET /api/bargains` — historical below-reference sales with days-listed
- `GET /api/hits` — hits from the most recent run
- `GET /api/buckets` — sold-price buckets (count/min/max/mean)

## Configuration (env)

| Variable | Default | Meaning |
|---|---|---|
| `IFYND_DB_PATH` | `ifynd.db` | SQLite path (`/data/ifynd.db` in Docker) |
| `IFYND_INTERVAL` | `30m` | Scrape interval |
| `IFYND_THRESHOLD_PCT` | `15` | Min % below reference to count as a hit |
| `IFYND_MIN_SAMPLES` | `5` | Min sold records before trusting a bucket |
| `IFYND_MIN_PRICE` | `100` | Skip listings priced below this (junk/scams) |
| `IFYND_METRIC` | `median` | `median` or `trimmed_mean` |
| `IFYND_TRIM_PCT` | `10` | Trim per tail for `trimmed_mean` |
| `IFYND_LOOKBACK_DAYS` | `90` | Sold-history window for references |
| `IFYND_SOLD_WINDOW_DAYS` | `14` | Incremental sold-scrape depth |
| `IFYND_SOLD_MAX_PAGES` | `20` | Page cap per incremental sold scrape |
| `IFYND_BACKFILL_PAGES` | `100` | Page cap for first-run backfill |
| `IFYND_ACTIVE_MAX_PAGES` | `25` | Page cap for active scrape |
| `IFYND_REQUEST_DELAY` | `1500ms` | Delay between page fetches (+ jitter) |
| `IFYND_NOTIFIER` | `log` | Notification channel |
| `IFYND_HTTP_ADDR` | `:8080` | API listen address |
| `IFYND_CATEGORY` | `340186` | Tradera category (iPhone) |
