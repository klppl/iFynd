package tradera

import (
	"os"
	"testing"
	"time"
)

// testdata/sold_page.html is a real (trimmed) capture of
// /category/340186?itemStatus=Sold from 2026-07-04.
func TestParseSearchPage(t *testing.T) {
	html, err := os.ReadFile("testdata/sold_page.html")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseSearchPage(html)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 80 {
		t.Fatalf("items = %d, want 80", len(res.Items))
	}
	if res.TotalItemCount != 7573 {
		t.Errorf("totalItemCount = %d, want 7573", res.TotalItemCount)
	}
	if res.PageCount != 95 {
		t.Errorf("pageCount = %d, want 95", res.PageCount)
	}

	first := res.Items[0]
	if first.ItemID != 725834341 {
		t.Errorf("first itemId = %d, want 725834341", first.ItemID)
	}
	if first.Price != 11000 {
		t.Errorf("first price = %d, want 11000", first.Price)
	}
	if first.ItemType != "Auction" {
		t.Errorf("first itemType = %q, want Auction", first.ItemType)
	}
	wantEnd := time.Date(2026, 4, 14, 17, 53, 1, 728000000, time.UTC)
	if !first.EndDate.Equal(wantEnd) {
		t.Errorf("first endDate = %v, want %v", first.EndDate, wantEnd)
	}
	if got := first.Attr("mobile_model"); got != "iPhone 16 Pro Max" {
		t.Errorf("first mobile_model = %q, want iPhone 16 Pro Max", got)
	}
	if got := first.Attr("mobile_disk_memory"); got != "1 TB" {
		t.Errorf("first mobile_disk_memory = %q, want 1 TB", got)
	}
	if first.ItemURL == "" || first.ShortDescription == "" {
		t.Error("first item missing url or title")
	}

	for _, it := range res.Items {
		if it.ItemID == 0 {
			t.Fatalf("item with zero id: %+v", it)
		}
	}
}

func TestPageURL(t *testing.T) {
	q := SoldQuery(340186)
	if got := q.pageURL(1, 0); got != "https://www.tradera.com/category/340186?itemStatus=Sold&sortBy=AddedOn" {
		t.Errorf("page 1 url = %s", got)
	}
	if got := q.pageURL(3, 7573); got != "https://www.tradera.com/category/340186?itemStatus=Sold&paging=3.a7573.s0&sortBy=AddedOn" {
		t.Errorf("page 3 url = %s", got)
	}
}
