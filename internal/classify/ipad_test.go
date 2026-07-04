package classify

import (
	"strings"
	"testing"
)

// Titles below are real listings captured from Tradera category 342496
// (iPad) on 2026-07-05.
func TestClassifyIPadFromTitle(t *testing.T) {
	tests := []struct {
		title string
		model string
		gb    int
	}{
		{"iPad Air 2 9.7” tum 16GB 2014 Rymdgrå Grade A, 100% Batterihälsa", "iPad Air 2", 16},
		{"iPad mini 4:e generationen 128 GB Space Gray", "iPad mini 4", 128},
		{"iPad (sjunde generationen) 128 GB", "iPad 7", 128},
		{"iPad 11 gen. 128GB", "iPad 11", 128},
		{"Oöppnad iPad 11 Wifi 128GB - Silver (Kvitto+Garanti)", "iPad 11", 128},
		{"iPad A16 Wi-Fi + Cellular 128 GB Ny oöppnad", "iPad 11", 128},
		{"iPad mini (A17 Pro) – 128GB – Wi-Fi – 9 Months Warranty", "iPad mini 7", 128},
		{"iPad Air 11-inch (M4) Wi-Fi + Cellular 128GB", "iPad Air 11 (M4)", 128},
		{"iPad Air 11 128gb M2 WiFi", "iPad Air 11 (M2)", 128},
		{"Apple iPad Mini (6:e generationen), 64 gb, Wifi", "iPad mini 6", 64},
		{"iPad Mini 5 64 GB wifi 2019", "iPad mini 5", 64},
		{"iPad 6 generation 128G fint full fungerande jätte fint", "iPad 6", 128},
		{"iPad 5 generation 128G fina full fungerande", "iPad 5", 128},
		{"Apple iPad 4 16gb", "iPad 4", 16},
		{"Silver iPad 9 64GB WiFi", "iPad 9", 64},
		{"Apple iPad 9 256gb", "iPad 9", 256},
		{"iPad | Apple iPad Mini 2 | Cellular | 32 GB | Modell A1490", "iPad mini 2", 32},
		{"iPad (8th generation) Wi-Fi 32 GB", "iPad 8", 32},
		{"Apple iPad (8:e generationen) Wi-Fi 32 GB 10.2-inch skärm", "iPad 8", 32},
		{"iPad Pro 11 tum (3:e generationen) 256GB", "iPad Pro 11 (gen 3)", 256},
		{"iPad Pro generation 2 11” 256gb", "iPad Pro 11 (gen 2)", 256},
		{"iPad Pro 12.9 M1 2021 256GB Wi-Fi", "iPad Pro 12.9 (gen 5)", 256},
		{"iPad Pro 10.5 64GB WiFi", "iPad Pro 10.5", 64},
		{"iPad 2018 32GB", "iPad 6", 32},
		{"iPad Air (2024) 11 tum 128GB + Magic Keyboard Air", "iPad Air 11 (M2)", 128},
	}
	for _, tt := range tests {
		res, ok, reason := Item(item(tt.title, nil), IPad)
		if !ok {
			t.Errorf("%q: rejected (%s), want %s/%d", tt.title, reason, tt.model, tt.gb)
			continue
		}
		if res.Model != tt.model || res.StorageGB != tt.gb {
			t.Errorf("%q: got %s/%d, want %s/%d", tt.title, res.Model, res.StorageGB, tt.model, tt.gb)
		}
	}
}

func TestClassifyIPadRejects(t *testing.T) {
	tests := []string{
		"iPad Pro 11 tum (3:e generationen) A2459 defekt reservdelar läs texten", // broken
		"iPad Pro 12.9 - Wi-Fi + Cellular - 512GB",                               // six 12.9" generations, none named
		"iPad Pro 12.9” / 32Gb / Wifi Only",                                      // same
		"iPad Pro med tangentbord och penna",                                     // no size or generation
		"iPad Pro 11 + Pencil PRO",                                               // no generation
		"Apple iPad Pro Gen4 (A2229) E0190620",                                   // generation but no size
		"iPad",                                                                   // nothing to go on
		"Apple iPad 9:e Gen 10.2\"",                                              // model fine but no storage
		"iPad Air 2 batteri (oanvänt)",                                           // spare battery
		"2 stycken iPad 7th Generation WiFi",                                     // multi-lot
		"5 pack reservdels ipads!",                                               // parts lot
		"iPad mini a1455",                                                        // only a hardware code
		"iPhone 13 Pro 128GB",                                                    // wrong family
		"iPad Air M3 13 128, som ny!",                                            // storage lacks a unit
		"Samsung Galaxy Tab S9 256GB",                                            // not an iPad
	}
	for _, title := range tests {
		if res, ok, _ := Item(item(title, nil), IPad); ok {
			t.Errorf("%q: classified as %s/%d, want reject", title, res.Model, res.StorageGB)
		}
	}

	// tablet_brand attribute rejects non-Apple even when the title mentions iPad.
	if _, ok, _ := Item(item("Surfplatta som iPad 64GB", map[string]string{"tablet_brand": "Samsung"}), IPad); ok {
		t.Error("non-Apple tablet_brand should reject")
	}
}

// Tradera's iPad category uses the brand facet for the family: values are
// "iPad", "iPad Air", "iPad Pro", "iPad mini" — never "Apple". Accept them,
// and treat facet-vs-title family disagreement as a red flag.
func TestClassifyIPadFamilyFacet(t *testing.T) {
	res, ok, reason := Item(item("iPad Air 2 64GB fint skick", map[string]string{"tablet_brand": "iPad Air"}), IPad)
	if !ok || res.Model != "iPad Air 2" {
		t.Errorf("matching facet: got %+v ok=%v (%s)", res, ok, reason)
	}
	res, ok, reason = Item(item("iPad 9 64GB", map[string]string{"tablet_brand": "iPad"}), IPad)
	if !ok || res.Model != "iPad 9" {
		t.Errorf("base facet: got %+v ok=%v (%s)", res, ok, reason)
	}

	if _, ok, reason := Item(item("iPad Pro 11 (3:e gen) 256GB", map[string]string{"tablet_brand": "iPad mini"}), IPad); ok || !strings.Contains(reason, "family mismatch") {
		t.Errorf("mini facet vs Pro title: ok=%v reason=%q, want family mismatch", ok, reason)
	}
	// The bare "iPad" facet is a seller default, not a claim — a more
	// specific title wins (observed with bulk "iPad Air 2" sellers).
	res, ok, reason = Item(item("iPad Air 2 64GB", map[string]string{"tablet_brand": "iPad"}), IPad)
	if !ok || res.Model != "iPad Air 2" {
		t.Errorf("generic facet + Air title: got %+v ok=%v (%s)", res, ok, reason)
	}
}

func TestClassifyMultiLot(t *testing.T) {
	// Real case: four iPads sold as one 900 kr listing.
	if _, ok, _ := Item(item("iPad 7th Gen och 3 iPad gen 5 128gb", nil), IPad); ok {
		t.Error("multi-device lot should reject")
	}
	if _, ok, _ := Item(item("2 st iPhone 12 64GB", nil), IPhone); ok {
		t.Error("multi-phone lot should reject")
	}
	// Years must not trigger the lot rule.
	if _, ok, reason := Item(item("2021 iPad Pro 11 M1 256GB", nil), IPad); !ok {
		t.Errorf("year prefix wrongly rejected: %s", reason)
	}
}
