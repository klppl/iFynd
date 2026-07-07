// Package store persists listings in SQLite (modernc.org/sqlite, no CGO).
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS sold_listings (
	id         INTEGER PRIMARY KEY,          -- Tradera listing id
	model      TEXT    NOT NULL,
	storage_gb INTEGER NOT NULL,
	price      INTEGER NOT NULL,             -- SEK
	title      TEXT    NOT NULL,             -- raw title, for auditing classification
	sold_at    TEXT    NOT NULL,             -- RFC3339
	listed_at  TEXT,                         -- RFC3339; NULL on rows scraped before the column existed
	url        TEXT    NOT NULL,
	scraped_at TEXT    NOT NULL,
	category   INTEGER NOT NULL DEFAULT 340186
);
CREATE INDEX IF NOT EXISTS idx_sold_bucket ON sold_listings(model, storage_gb, sold_at);

CREATE TABLE IF NOT EXISTS active_listings (
	id         INTEGER PRIMARY KEY,
	model      TEXT    NOT NULL,
	storage_gb INTEGER NOT NULL,
	price      INTEGER NOT NULL,             -- fixed "köp nu" price, SEK
	title      TEXT    NOT NULL,
	url        TEXT    NOT NULL,
	first_seen TEXT    NOT NULL,
	last_seen  TEXT    NOT NULL,
	notified   INTEGER NOT NULL DEFAULT 0,
	broken     INTEGER NOT NULL DEFAULT 0,   -- user-flagged in the GUI; excluded from hits
	category   INTEGER NOT NULL DEFAULT 340186,
	listed_at  TEXT                          -- Tradera startDate; NULL only on pre-migration rows
);

CREATE TABLE IF NOT EXISTS blocked_listings (
	id         INTEGER PRIMARY KEY,         -- Tradera listing id
	reason     TEXT NOT NULL,               -- broken | excluded
	title      TEXT NOT NULL,
	url        TEXT NOT NULL,
	blocked_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS skipped_listings (
	id       INTEGER PRIMARY KEY,
	source   TEXT NOT NULL,                  -- sold | active
	title    TEXT NOT NULL,
	url      TEXT NOT NULL,
	reason   TEXT NOT NULL,
	seen_at  TEXT NOT NULL
);

-- Admin-editable tuning, overriding the IFYND_* env defaults at runtime.
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Notification channels (Discord/ntfy/Gotify/webhook), any number enabled.
CREATE TABLE IF NOT EXISTS notify_channels (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	kind    TEXT    NOT NULL,               -- discord | ntfy | gotify | webhook
	name    TEXT    NOT NULL DEFAULT '',    -- user label
	url     TEXT    NOT NULL DEFAULT '',    -- webhook/topic/server URL
	token   TEXT    NOT NULL DEFAULT '',    -- gotify app token / ntfy auth, optional
	enabled INTEGER NOT NULL DEFAULT 1
);

-- Watchlist: only listings matching an enabled alert are notified.
CREATE TABLE IF NOT EXISTS alerts (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	match_type    TEXT    NOT NULL,             -- model | generation
	pattern       TEXT    NOT NULL,             -- "iPhone 16 Pro" or "iPhone 16"
	storage_gb    INTEGER NOT NULL DEFAULT 0,   -- 0 = any storage
	min_pct_below REAL    NOT NULL DEFAULT 0,   -- 0 = use the global threshold
	max_price     INTEGER NOT NULL DEFAULT 0,   -- 0 = no absolute ceiling
	enabled       INTEGER NOT NULL DEFAULT 1,
	created_at    TEXT    NOT NULL
);
`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; the app is one goroutine + read-only HTTP handlers
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Migrations for databases created before a column existed.
	addedColumns := []struct{ table, col, def string }{
		{"active_listings", "broken", "INTEGER NOT NULL DEFAULT 0"},
		{"sold_listings", "listed_at", "TEXT"},
		// 340186 (iPhone) was the only category before multi-category support.
		{"sold_listings", "category", "INTEGER NOT NULL DEFAULT 340186"},
		{"active_listings", "category", "INTEGER NOT NULL DEFAULT 340186"},
		{"active_listings", "listed_at", "TEXT"}, // refreshed on next scrape
	}
	for _, m := range addedColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, m.table, m.col).Scan(&n); err == nil && n == 0 {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, m.table, m.col, m.def)); err != nil {
				db.Close()
				return nil, fmt.Errorf("migrate %s.%s: %w", m.table, m.col, err)
			}
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type SoldListing struct {
	ID        int64
	Model     string
	StorageGB int
	Price     int
	Title     string
	SoldAt    time.Time
	ListedAt  time.Time // zero when unknown (rows from before the column existed)
	URL       string
	Category  int
}

// InsertSold appends a sold record; returns false if the id already existed.
// Re-scraping a known id fills in listed_at if an older run left it NULL.
func (s *Store) InsertSold(l SoldListing) (bool, error) {
	var listedAt any
	if !l.ListedAt.IsZero() {
		listedAt = l.ListedAt.UTC().Format(time.RFC3339)
	}
	res, err := s.db.Exec(`INSERT INTO sold_listings
		(id, model, storage_gb, price, title, sold_at, listed_at, url, scraped_at, category)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET listed_at = excluded.listed_at
			WHERE sold_listings.listed_at IS NULL AND excluded.listed_at IS NOT NULL`,
		l.ID, l.Model, l.StorageGB, l.Price, l.Title,
		l.SoldAt.UTC().Format(time.RFC3339), listedAt, l.URL, time.Now().UTC().Format(time.RFC3339), l.Category)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListSold returns sold listings since the cutoff, newest sale first.
func (s *Store) ListSold(since time.Time) ([]SoldListing, error) {
	rows, err := s.db.Query(`SELECT id, model, storage_gb, price, title, sold_at, listed_at, url
		FROM sold_listings WHERE sold_at >= ? ORDER BY sold_at DESC`,
		since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoldListing
	for rows.Next() {
		var l SoldListing
		var soldAt string
		var listedAt sql.NullString
		if err := rows.Scan(&l.ID, &l.Model, &l.StorageGB, &l.Price, &l.Title, &soldAt, &listedAt, &l.URL); err != nil {
			return nil, err
		}
		l.SoldAt, _ = time.Parse(time.RFC3339, soldAt)
		if listedAt.Valid {
			l.ListedAt, _ = time.Parse(time.RFC3339, listedAt.String)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) SoldCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sold_listings`).Scan(&n)
	return n, err
}

// SoldCountCategory drives the per-category backfill decision.
func (s *Store) SoldCountCategory(categoryID int) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sold_listings WHERE category = ?`, categoryID).Scan(&n)
	return n, err
}

type ActiveListing struct {
	ID        int64
	Model     string
	StorageGB int
	Price     int
	Title     string
	URL       string
	FirstSeen time.Time
	LastSeen  time.Time
	ListedAt  time.Time // Tradera's startDate; zero on pre-migration rows
	Notified  bool
	Broken    bool
	Category  int
}

// UpsertActive inserts or refreshes an active listing. Price updates on
// conflict (sellers adjust prices); first_seen and notified are preserved.
func (s *Store) UpsertActive(l ActiveListing) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var listedAt any
	if !l.ListedAt.IsZero() {
		listedAt = l.ListedAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`INSERT INTO active_listings
		(id, model, storage_gb, price, title, url, first_seen, last_seen, category, listed_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			price = excluded.price,
			title = excluded.title,
			last_seen = excluded.last_seen,
			listed_at = COALESCE(excluded.listed_at, active_listings.listed_at)`,
		l.ID, l.Model, l.StorageGB, l.Price, l.Title, l.URL, now, now, l.Category, listedAt)
	return err
}

// Flags returns the notified and broken flags for a listing.
func (s *Store) Flags(id int64) (notified, broken bool, err error) {
	var n, b int
	err = s.db.QueryRow(`SELECT notified, broken FROM active_listings WHERE id = ?`, id).Scan(&n, &b)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	return n != 0, b != 0, err
}

// SetBroken flags or unflags a listing as a broken device. A broken listing
// stays visible in the active table but is tombstoned in blocked_listings so
// its price never enters the sold history when the phone eventually sells.
func (s *Store) SetBroken(id int64, broken bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var title, url string
	if err := tx.QueryRow(`SELECT title, url FROM active_listings WHERE id = ?`, id).Scan(&title, &url); err != nil {
		return err // sql.ErrNoRows for unknown ids
	}
	if _, err := tx.Exec(`UPDATE active_listings SET broken = ? WHERE id = ?`, broken, id); err != nil {
		return err
	}
	if broken {
		_, err = tx.Exec(`INSERT OR REPLACE INTO blocked_listings (id, reason, title, url, blocked_at)
			VALUES (?, 'broken', ?, ?, ?)`, id, title, url, time.Now().UTC().Format(time.RFC3339))
	} else {
		_, err = tx.Exec(`DELETE FROM blocked_listings WHERE id = ? AND reason = 'broken'`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Exclude deletes a listing from the active table and tombstones it so no
// future scrape (active or sold) ever brings it back.
func (s *Store) Exclude(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var title, url string
	if err := tx.QueryRow(`SELECT title, url FROM active_listings WHERE id = ?`, id).Scan(&title, &url); err != nil {
		return err // sql.ErrNoRows for unknown ids
	}
	if _, err := tx.Exec(`DELETE FROM active_listings WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO blocked_listings (id, reason, title, url, blocked_at)
		VALUES (?, 'excluded', ?, ?, ?)`, id, title, url, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// BlockedIDs returns every tombstoned listing id mapped to its reason.
func (s *Store) BlockedIDs() (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT id, reason FROM blocked_listings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var reason string
		if err := rows.Scan(&id, &reason); err != nil {
			return nil, err
		}
		out[id] = reason
	}
	return out, rows.Err()
}

// ListActive returns all active listings, newest first.
func (s *Store) ListActive() ([]ActiveListing, error) {
	rows, err := s.db.Query(`SELECT id, model, storage_gb, price, title, url,
		first_seen, last_seen, listed_at, notified, broken
		FROM active_listings ORDER BY first_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveListing
	for rows.Next() {
		var l ActiveListing
		var first, last string
		var listed sql.NullString
		var notified, broken int
		if err := rows.Scan(&l.ID, &l.Model, &l.StorageGB, &l.Price, &l.Title, &l.URL,
			&first, &last, &listed, &notified, &broken); err != nil {
			return nil, err
		}
		l.FirstSeen, _ = time.Parse(time.RFC3339, first)
		l.LastSeen, _ = time.Parse(time.RFC3339, last)
		if listed.Valid {
			l.ListedAt, _ = time.Parse(time.RFC3339, listed.String)
		}
		l.Notified = notified != 0
		l.Broken = broken != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) MarkNotified(id int64) error {
	_, err := s.db.Exec(`UPDATE active_listings SET notified = 1 WHERE id = ?`, id)
	return err
}

// PruneActive removes listings not seen since the cutoff (ended or sold).
func (s *Store) PruneActive(olderThan time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM active_listings WHERE last_seen < ?`,
		olderThan.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneCategories deletes sold and active listings whose category is not in
// keep — used to purge a retired family (e.g. iPad/MacBook) from an existing
// DB after it's dropped from IFYND_CATEGORIES. skipped_listings is audit-only
// and has no category column, so it's left untouched.
func (s *Store) PruneCategories(keep []int) (sold, active int64, err error) {
	if len(keep) == 0 {
		return 0, 0, fmt.Errorf("refusing to prune: no categories to keep")
	}
	ph := make([]string, len(keep))
	args := make([]any, len(keep))
	for i, id := range keep {
		ph[i] = "?"
		args[i] = id
	}
	in := "(" + strings.Join(ph, ",") + ")"
	for _, t := range []struct {
		table string
		out   *int64
	}{{"sold_listings", &sold}, {"active_listings", &active}} {
		res, err := s.db.Exec(`DELETE FROM `+t.table+` WHERE category NOT IN `+in, args...)
		if err != nil {
			return sold, active, fmt.Errorf("prune %s: %w", t.table, err)
		}
		*t.out, _ = res.RowsAffected()
	}
	return sold, active, nil
}

// RecordSkipped logs an unclassifiable listing for later audit.
func (s *Store) RecordSkipped(id int64, source, title, url, reason string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO skipped_listings
		(id, source, title, url, reason, seen_at) VALUES (?,?,?,?,?,?)`,
		id, source, title, url, reason, time.Now().UTC().Format(time.RFC3339))
	return err
}

// SoldPrices returns all sold prices for a bucket since the cutoff.
func (s *Store) SoldPrices(model string, storageGB int, since time.Time) ([]float64, error) {
	rows, err := s.db.Query(`SELECT price FROM sold_listings
		WHERE model = ? AND storage_gb = ? AND sold_at >= ?`,
		model, storageGB, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prices []float64
	for rows.Next() {
		var p float64
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		prices = append(prices, p)
	}
	return prices, rows.Err()
}

// BucketRow is one (model, storage) aggregate for the HTTP API.
type BucketRow struct {
	Model     string  `json:"model"`
	StorageGB int     `json:"storage_gb"`
	Samples   int     `json:"samples"`
	MinPrice  int     `json:"min_price"`
	MaxPrice  int     `json:"max_price"`
	MeanPrice float64 `json:"mean_price"`
}

func (s *Store) Buckets(since time.Time) ([]BucketRow, error) {
	rows, err := s.db.Query(`SELECT model, storage_gb, COUNT(*), MIN(price), MAX(price), AVG(price)
		FROM sold_listings WHERE sold_at >= ?
		GROUP BY model, storage_gb ORDER BY model, storage_gb`,
		since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BucketRow
	for rows.Next() {
		var b BucketRow
		if err := rows.Scan(&b.Model, &b.StorageGB, &b.Samples, &b.MinPrice, &b.MaxPrice, &b.MeanPrice); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Settings returns every admin-set key/value override.
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting upserts one tuning override.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Channel is one configured notification target.
type Channel struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"` // discord | ntfy | gotify | webhook
	Name    string `json:"name"`
	URL     string `json:"url"`
	Token   string `json:"token"`
	Enabled bool   `json:"enabled"`
}

func (s *Store) Channels() ([]Channel, error) {
	rows, err := s.db.Query(`SELECT id, kind, name, url, token, enabled FROM notify_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var c Channel
		var enabled int
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.URL, &c.Token, &enabled); err != nil {
			return nil, err
		}
		c.Enabled = enabled != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertChannel inserts (id==0) or updates a channel, returning its id.
func (s *Store) UpsertChannel(c Channel) (int64, error) {
	if c.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO notify_channels (kind, name, url, token, enabled)
			VALUES (?,?,?,?,?)`, c.Kind, c.Name, c.URL, c.Token, b2i(c.Enabled))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE notify_channels SET kind=?, name=?, url=?, token=?, enabled=? WHERE id=?`,
		c.Kind, c.Name, c.URL, c.Token, b2i(c.Enabled), c.ID)
	return c.ID, err
}

func (s *Store) DeleteChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM notify_channels WHERE id = ?`, id)
	return err
}

// Alert is one watchlist rule. Only listings matching an enabled alert are
// notified (watchlist-only mode).
type Alert struct {
	ID          int64   `json:"id"`
	MatchType   string  `json:"match_type"` // model | generation
	Pattern     string  `json:"pattern"`
	StorageGB   int     `json:"storage_gb"`
	MinPctBelow float64 `json:"min_pct_below"`
	MaxPrice    int     `json:"max_price"`
	Enabled     bool    `json:"enabled"`
}

func (s *Store) Alerts() ([]Alert, error) {
	rows, err := s.db.Query(`SELECT id, match_type, pattern, storage_gb, min_pct_below, max_price, enabled
		FROM alerts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var enabled int
		if err := rows.Scan(&a.ID, &a.MatchType, &a.Pattern, &a.StorageGB, &a.MinPctBelow, &a.MaxPrice, &enabled); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertAlert inserts (id==0) or updates a watchlist rule, returning its id.
func (s *Store) UpsertAlert(a Alert) (int64, error) {
	if a.ID == 0 {
		res, err := s.db.Exec(`INSERT INTO alerts
			(match_type, pattern, storage_gb, min_pct_below, max_price, enabled, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			a.MatchType, a.Pattern, a.StorageGB, a.MinPctBelow, a.MaxPrice, b2i(a.Enabled),
			time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.Exec(`UPDATE alerts SET match_type=?, pattern=?, storage_gb=?, min_pct_below=?, max_price=?, enabled=? WHERE id=?`,
		a.MatchType, a.Pattern, a.StorageGB, a.MinPctBelow, a.MaxPrice, b2i(a.Enabled), a.ID)
	return a.ID, err
}

func (s *Store) DeleteAlert(id int64) error {
	_, err := s.db.Exec(`DELETE FROM alerts WHERE id = ?`, id)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
