package classify

import (
	"strings"
	"testing"

	"github.com/klppl/ifynd/internal/tradera"
)

func item(title string, attrs map[string]string) *tradera.Item {
	it := &tradera.Item{ShortDescription: title}
	for k, v := range attrs {
		it.Attributes = append(it.Attributes, tradera.Attribute{Name: k, Values: []string{v}})
	}
	return it
}

// Titles below are real listings captured from Tradera on 2026-07-04.
func TestClassifyFromTitle(t *testing.T) {
	tests := []struct {
		title string
		model string
		gb    int
	}{
		{"iPhone 13 Pro - 256 GB - Bra skick (renoverad)", "iPhone 13 Pro", 256},
		{"iPhone 14 Pro Ny Skick! 128GB, 100% batterihälsa", "iPhone 14 Pro", 128},
		{"iPhone 13 Pro Max 256 GB 98% batterihälsa", "iPhone 13 Pro Max", 256},
		{"iPhone 12 mini 128GB Blå", "iPhone 12 mini", 128},
		{"iPhone 15 plus 128gb", "iPhone 15 Plus", 128},
		{"iPhone 11 Pro Max 64GB Guld", "iPhone 11 Pro Max", 64},
		{"iPhone 8, 256 GB", "iPhone 8", 256},
		{"IPhone XS max perfekt skick 64 GB", "iPhone XS Max", 64},
		{"iPhone 17 PRO Max 512gb", "iPhone 17 Pro Max", 512},
		{"iPhone 16 pro Max 1tb / 24 Mån Garanti", "iPhone 16 Pro Max", 1024},
		{"iPhone 6 16GB", "iPhone 6", 16},
		{"Apple iPhone 14 Pro 128GB", "iPhone 14 Pro", 128},
		{"iPhone 12, 64 GB, 100% batterihälsa", "iPhone 12", 64},
	}
	for _, tt := range tests {
		res, ok, reason := Item(item(tt.title, nil), IPhone)
		if !ok {
			t.Errorf("%q: rejected (%s), want %s/%d", tt.title, reason, tt.model, tt.gb)
			continue
		}
		if res.Model != tt.model || res.StorageGB != tt.gb {
			t.Errorf("%q: got %s/%d, want %s/%d", tt.title, res.Model, res.StorageGB, tt.model, tt.gb)
		}
	}
}

func TestClassifyRejects(t *testing.T) {
	tests := []string{
		"GRYMT PAKET! iPhone 15, Apple Watch 9 och AirPods Pro", // bundle
		"iPhone 6S Rosa – Defekt, sprucken skärm, iCloud-låst",  // broken
		"Skal till iPhone 13 Pro",                               // accessory
		"iPhone 12",                                             // no storage
		"iPhone 6 126 GB Gold komplett i original box",          // 126 GB is a typo, not a real size
		"Orange smartphone med tre kameror",                     // no iPhone model
		"iPhone SE 64GB",                                        // SE without generation year
		"Säljer iPhone 13 128GB och iPhone 12 64GB",             // two storages
		"Tom kartong till iPhone 15 Pro 256GB",                  // empty box
		"iPhone 14 Pro Max cracked back working 128gb",          // damaged, English
		"iPhone Xs Max 256 GB ser ut som iPhone 17 PRO Max",     // lookalike
		"Extra Frakt Ordernr. 147535957",                        // shipping surcharge
		"KARTONG iPhone 14 Pro 128GB Space Black",               // empty box, prefix
		"iPhone 13 Pro Max 128gb defekt",                        // damaged, Swedish
	}
	for _, title := range tests {
		if res, ok, _ := Item(item(title, nil), IPhone); ok {
			t.Errorf("%q: classified as %s/%d, want reject", title, res.Model, res.StorageGB)
		}
	}
}

func TestClassifyPrefersAttributes(t *testing.T) {
	it := item("Grym mobil säljes!", map[string]string{
		"mobile_brand":       "Apple",
		"mobile_model":       "iPhone 14 Pro Max",
		"mobile_disk_memory": "1 TB",
		"condition":          "Mycket gott skick",
	})
	res, ok, reason := Item(it, IPhone)
	if !ok {
		t.Fatalf("rejected: %s", reason)
	}
	if res.Model != "iPhone 14 Pro Max" || res.StorageGB != 1024 {
		t.Errorf("got %s/%d, want iPhone 14 Pro Max/1024", res.Model, res.StorageGB)
	}

	if _, ok, _ := Item(item("iPhone 13 128GB", map[string]string{"condition": "Defekt"}), IPhone); ok {
		t.Error("defekt condition should reject")
	}
	if _, ok, _ := Item(item("Galaxy S23 128GB", map[string]string{"mobile_brand": "Samsung"}), IPhone); ok {
		t.Error("non-Apple brand should reject")
	}

	// SE with generation from attributes is fine even though title-only SE is not.
	res, ok, _ = Item(item("iPhone SE fint skick 64GB", map[string]string{"mobile_model": "iPhone SE (2020)"}), IPhone)
	if !ok || res.Model != "iPhone SE (2020)" {
		t.Errorf("SE attr: got %+v ok=%v", res, ok)
	}
}

// Real case: seller filed an "iPhone 16 Plus" title under the "iPhone 16"
// model attribute. When both sources parse confidently but disagree, skip.
func TestClassifyAttrTitleMismatch(t *testing.T) {
	_, ok, reason := Item(item("iPhone 16 Plus 128 gb Rosa", map[string]string{"mobile_model": "iPhone 16"}), IPhone)
	if ok {
		t.Error("model mismatch should reject")
	} else if !strings.Contains(reason, "model mismatch") {
		t.Errorf("reason = %q, want model mismatch", reason)
	}

	_, ok, reason = Item(item("iPhone 13 256GB", map[string]string{"mobile_disk_memory": "128 GB"}), IPhone)
	if ok {
		t.Error("storage mismatch should reject")
	} else if !strings.Contains(reason, "storage mismatch") {
		t.Errorf("reason = %q, want storage mismatch", reason)
	}

	// Bare "iPhone" attribute carries no information — title wins, no mismatch.
	res, ok, reason := Item(item("iPhone 16 PRO 128 gb", map[string]string{"mobile_model": "iPhone"}), IPhone)
	if !ok || res.Model != "iPhone 16 Pro" {
		t.Errorf("bare iPhone attr: got %+v ok=%v (%s)", res, ok, reason)
	}
	// Bare "iPhone SE" attr + year from title = added specificity, not conflict.
	res, ok, reason = Item(item("iPhone SE 2020 64GB", map[string]string{"mobile_model": "iPhone SE"}), IPhone)
	if !ok || res.Model != "iPhone SE (2020)" {
		t.Errorf("SE year from title: got %+v ok=%v (%s)", res, ok, reason)
	}

	// Agreement (case-insensitive) still classifies.
	res, ok, reason = Item(item("iphone 13 pro max 256gb", map[string]string{
		"mobile_model": "iPhone 13 Pro Max", "mobile_disk_memory": "256 GB"}), IPhone)
	if !ok || res.Model != "iPhone 13 Pro Max" || res.StorageGB != 256 {
		t.Errorf("agreeing sources: got %+v ok=%v (%s)", res, ok, reason)
	}
}
