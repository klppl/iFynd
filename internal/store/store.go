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
	notified   INTEGER NOT NULL DEFAULT 0
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
	URL       string
}

// InsertSold appends a sold record; returns false if the id already existed.
func (s *Store) InsertSold(l SoldListing) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO sold_listings
		(id, model, storage_gb, price, title, sold_at, url, scraped_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		l.ID, l.Model, l.StorageGB, l.Price, l.Title,
		l.SoldAt.UTC().Format(time.RFC3339), l.URL, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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
	Notified  bool
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

func (s *Store) IsNotified(id int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT notified FROM active_listings WHERE id = ?`, id).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return n != 0, err
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
