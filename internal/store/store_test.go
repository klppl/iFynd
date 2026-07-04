package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBrokenAndExcludeTombstones(t *testing.T) {
	s := testStore(t)
	l := ActiveListing{ID: 1, Model: "iPhone 13", StorageGB: 128, Price: 2500,
		Title: "iPhone 13 128GB", URL: "https://example.com/1"}
	if err := s.UpsertActive(l); err != nil {
		t.Fatal(err)
	}
	l2 := l
	l2.ID, l2.URL = 2, "https://example.com/2"
	if err := s.UpsertActive(l2); err != nil {
		t.Fatal(err)
	}

	// Broken: flagged, tombstoned, and survives a re-upsert.
	if err := s.SetBroken(1, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertActive(l); err != nil { // scrape refreshes the row
		t.Fatal(err)
	}
	if _, broken, _ := s.Flags(1); !broken {
		t.Error("broken flag lost after upsert")
	}
	blocked, err := s.BlockedIDs()
	if err != nil {
		t.Fatal(err)
	}
	if blocked[1] != "broken" {
		t.Errorf("blocked[1] = %q, want broken", blocked[1])
	}

	// Ångra removes the tombstone again.
	if err := s.SetBroken(1, false); err != nil {
		t.Fatal(err)
	}
	if blocked, _ = s.BlockedIDs(); len(blocked) != 0 {
		t.Errorf("blocklist after undo = %v, want empty", blocked)
	}

	// Exclude: gone from actives, tombstoned as excluded.
	if err := s.Exclude(2); err != nil {
		t.Fatal(err)
	}
	actives, err := s.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actives {
		if a.ID == 2 {
			t.Error("excluded listing still in active table")
		}
	}
	if blocked, _ = s.BlockedIDs(); blocked[2] != "excluded" {
		t.Errorf("blocked[2] = %q, want excluded", blocked[2])
	}

	// Unknown ids surface as ErrNoRows for the 404 path.
	if err := s.Exclude(99); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Exclude(99) = %v, want ErrNoRows", err)
	}
	if err := s.SetBroken(99, true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("SetBroken(99) = %v, want ErrNoRows", err)
	}
}

func TestInsertSoldFillsListedAt(t *testing.T) {
	s := testStore(t)
	sold := SoldListing{ID: 7, Model: "iPhone 12", StorageGB: 64, Price: 1500,
		Title: "iPhone 12", SoldAt: time.Now(), URL: "u"}
	if ins, err := s.InsertSold(sold); err != nil || !ins {
		t.Fatalf("first insert: ins=%v err=%v", ins, err)
	}
	// Re-scrape with a listing date fills the NULL column.
	sold.ListedAt = time.Now().Add(-72 * time.Hour)
	if _, err := s.InsertSold(sold); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListSold(time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListSold: %v, n=%d", err, len(rows))
	}
	if rows[0].ListedAt.IsZero() {
		t.Error("listed_at not backfilled on re-insert")
	}
}
