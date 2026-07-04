// Package store persists listings in SQLite (modernc.org/sqlite, no CGO).
package store

import (
	"database/sql"
	"fmt"
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
	scraped_at TEXT    NOT NULL
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
	broken     INTEGER NOT NULL DEFAULT 0    -- user-flagged in the GUI; excluded from hits
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
}

// InsertSold appends a sold record; returns false if the id already existed.
// Re-scraping a known id fills in listed_at if an older run left it NULL.
func (s *Store) InsertSold(l SoldListing) (bool, error) {
	var listedAt any
	if !l.ListedAt.IsZero() {
		listedAt = l.ListedAt.UTC().Format(time.RFC3339)
	}
	res, err := s.db.Exec(`INSERT INTO sold_listings
		(id, model, storage_gb, price, title, sold_at, listed_at, url, scraped_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET listed_at = excluded.listed_at
			WHERE sold_listings.listed_at IS NULL AND excluded.listed_at IS NOT NULL`,
		l.ID, l.Model, l.StorageGB, l.Price, l.Title,
		l.SoldAt.UTC().Format(time.RFC3339), listedAt, l.URL, time.Now().UTC().Format(time.RFC3339))
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

type ActiveListing struct {
	ID        int64
	Model     string
	StorageGB int
	Price     int
	Title     string
	URL       string
	FirstSeen time.Time
	LastSeen  time.Time
	Notified  bool
	Broken    bool
}

// UpsertActive inserts or refreshes an active listing. Price updates on
// conflict (sellers adjust prices); first_seen and notified are preserved.
func (s *Store) UpsertActive(l ActiveListing) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`INSERT INTO active_listings
		(id, model, storage_gb, price, title, url, first_seen, last_seen)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			price = excluded.price,
			title = excluded.title,
			last_seen = excluded.last_seen`,
		l.ID, l.Model, l.StorageGB, l.Price, l.Title, l.URL, now, now)
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
		first_seen, last_seen, notified, broken
		FROM active_listings ORDER BY first_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActiveListing
	for rows.Next() {
		var l ActiveListing
		var first, last string
		var notified, broken int
		if err := rows.Scan(&l.ID, &l.Model, &l.StorageGB, &l.Price, &l.Title, &l.URL,
			&first, &last, &notified, &broken); err != nil {
			return nil, err
		}
		l.FirstSeen, _ = time.Parse(time.RFC3339, first)
		l.LastSeen, _ = time.Parse(time.RFC3339, last)
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
