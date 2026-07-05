package classify

import "testing"

// Titles below are real listings captured from Tradera's laptop category
// (302393, af-computer_brand=Apple) on 2026-07-05.
func TestClassifyMacBooks(t *testing.T) {
	tests := []struct {
		title string
		proc  string // computer_processor_model facet
		model string
		gb    int
	}{
		{"MacBook Pro 16 M1 Pro 32GB 500GB 98% batteri", "Apple", "MacBook Pro 16 M1 Pro", 512},
		{"MacBook Pro 16-tum Apple M4 Pro 24 GB RAM 1TB", "Apple", "MacBook Pro 16 M4 Pro", 1024},
		{"MacBook Air 13.6-inch M2, 2022, 8GB/256GB", "Apple", "MacBook Air 13 M2", 256},
		{"MacBook Air 13 tum M3 16 GB/ 256 GB", "Apple", "MacBook Air 13 M3", 256},
		{"Apple MacBook Air 13” 2015 8GB 256GB Core i5 1,6Ghz", "Intel", "MacBook Air 13 (2015)", 256},
		{"Apple MacBook Air 13” (2025, M4) – 16 GB RAM / 256 GB SSD", "Apple", "MacBook Air 13 M4", 256},
		{"MacBook Air M2 2022 256GB", "", "MacBook Air 13 M2", 256},
		{"MacBook Pro 14\" M3 Pro 18GB 512GB", "Apple", "MacBook Pro 14 M3 Pro", 512},
		{"Macbook Air M1 2020 8/256 gb", "Apple", "MacBook Air 13 M1", 256},
		{"MacBook Air 2020 13 tum 256GB SSD", "Intel", "MacBook Air 13 (2020)", 256},
		{"MacBook Pro 15,4 tum 2017 512GB", "Intel", "MacBook Pro 15 (2017)", 512},
		{"MacBook Pro 14 M1 Pro 16GB 512GB med laddare", "", "MacBook Pro 14 M1 Pro", 512},
		// Plain M1/M2 Pros only came as 13"; plain M3/M4 only as 14".
		{"MacBook Pro M1 2020 8GB 256GB", "Apple", "MacBook Pro 13 M1", 256},
		{"MacBook Pro M4 16GB RAM 512GB SSD", "Apple", "MacBook Pro 14 M4", 512},
		// The tier may arrive in a later chip mention than the first.
		{"MacBook Pro 14\" M1 2021 Apple M1 Pro 8-Core 14-Core GPU 16 GB RAM 2 TB SSD", "Apple", "MacBook Pro 14 M1 Pro", 2048},
		{"Apple MacBook Pro M5, 14-tum, 16GB/512GB. Nyskick! Applegaranti!", "Apple", "MacBook Pro 14 M5", 512},
	}
	for _, tt := range tests {
		attrs := map[string]string{}
		if tt.proc != "" {
			attrs["computer_processor_model"] = tt.proc
		}
		res, ok, reason := Item(item(tt.title, attrs), MacBook)
		if !ok {
			t.Errorf("%q: rejected (%s), want %s/%d", tt.title, reason, tt.model, tt.gb)
			continue
		}
		if res.Model != tt.model || res.StorageGB != tt.gb {
			t.Errorf("%q: got %s/%d, want %s/%d", tt.title, res.Model, res.StorageGB, tt.model, tt.gb)
		}
	}
}

func TestClassifyMacBookRejects(t *testing.T) {
	tests := []struct {
		title string
		proc  string
		why   string
	}{
		{"MacBook Pro 16\" (2021) med 16 GB RAM och 512 GB SSD", "Apple", "M-era year without chip: tier unknowable"},
		{"MacBook Air 2020 256GB", "", "2020 is Intel or M1; no facet to break the tie"},
		{"Apple Macbook Core i7", "Intel", "bare MacBook line"},
		{"Apple MacBook Air SuperDrive MC684ZM/A", "", "accessory"},
		{"MacBook Pro 13-tums, M1, 2020", "Apple", "no storage size"},
		{"MacBook Pro 15” 2017, säljes defekt/reservdelar", "Intel", "broken"},
		{"MacBook Pro 14 M3 512GB", "Intel", "chip vs processor facet mismatch"},
		{"Skal till MacBook Air 13", "", "accessory"},
		{"MacBook Air 13 tum", "", "no chip and no year"},
		{"2 st MacBook Pro 13 M1 256GB", "Apple", "multi-device lot"},
		{"MacBook Air M2 512GB", "", "13\" or 15\" — ambiguous"},
		{"MacBook Pro 16 M1 512GB", "Apple", "16\" with a plain chip: dropped tier"},
		{"MacBook Air M2 15 tum + iPhone 12 paket", "Apple", "bundle"},
		{"MacBook Pro 13 M1 256GB och 512GB", "Apple", "two storage claims"},
		{"Apple iBook G3 Clamshell Orange", "", "not a MacBook"},
		{"MacBook Pro 13 M1 eller M2 256GB", "Apple", "two different chips"},
	}
	for _, tt := range tests {
		attrs := map[string]string{}
		if tt.proc != "" {
			attrs["computer_processor_model"] = tt.proc
		}
		if res, ok, _ := Item(item(tt.title, attrs), MacBook); ok {
			t.Errorf("%q (%s): classified as %s/%d, want reject", tt.title, tt.why, res.Model, res.StorageGB)
		}
	}
}

// "med laddare" must stay junk for phones but not for laptops — chargers
// are routinely mentioned as included extras in laptop titles.
func TestAccessoryWordsPerFamily(t *testing.T) {
	if _, ok, _ := Item(item("iPhone 13 128GB med laddare", nil), IPhone); ok {
		t.Error("laddare should still reject an iPhone listing")
	}
	if _, ok, reason := Item(item("MacBook Air 13 M2 2022 256GB med laddare", nil), MacBook); !ok {
		t.Errorf("laddare should not reject a MacBook listing (%s)", reason)
	}
}
