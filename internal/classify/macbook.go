package classify

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// MacBook listings in Tradera's laptop category carry no model, storage or
// year attributes: computer_processor_model is just "Apple"/"Intel", the
// screen-size facet is a coarse range, and the RAM facet is often wrong
// (observed: an M4 Pro with "24 GB" in the title filed under "16 GB"). So
// the bucket comes from the title alone. Apple Silicon machines bucket as
// line + screen + chip ("MacBook Pro 14 M3 Pro"); Intel-era machines as
// line + screen + year ("MacBook Pro 13 (2015)"). RAM is deliberately NOT
// part of the bucket — it would spread samples below MIN_SAMPLES — but it
// must still be told apart from storage, since titles routinely name both
// ("16GB/512GB"). Anything underdetermined is skipped: a bare "MacBook"
// title, an M-era Pro without its chip tier, a 2020 machine that could be
// Intel or M1.

var (
	macChipRe = regexp.MustCompile(`\bm([1-6])\s*(pro|max|ultra)?\b`)
	macYearRe = regexp.MustCompile(`\b(20[0-2]\d)\b`)
	// "13 tum", "15,4 tum" — quote marks and "inch" are normalized to
	// " tum " before matching
	macSizeUnitRe = regexp.MustCompile(`\b(1[1-7])(?:[.,]\d)?\s*tum`)
	// The first number after the line word is the screen size ("MacBook
	// Pro 16 M1 Pro") — unless a GB/TB unit follows ("MacBook Pro 16GB
	// RAM"), which group 2 detects.
	macNumAfterLineRe = regexp.MustCompile(`\bmacbook\s+(?:air|pro)\s+\(?(1[1-7])(?:[.,]\d)?\s*(gb|tb)?\b`)
	macModelCodeRe    = regexp.MustCompile(`\ba\d{4}\b`) // "A1707"
	macMemRe          = regexp.MustCompile(`\b(\d{1,4})\s*(gb|tb)\b`)
)

// macRAMSizes are plausible RAM configurations, used to tell the smaller
// number in "8GB/256GB" apart from storage.
var macRAMSizes = map[int]bool{
	2: true, 4: true, 6: true, 8: true, 12: true, 16: true, 18: true,
	24: true, 32: true, 36: true, 48: true, 64: true, 96: true, 128: true,
}

// macStorageSizes maps a title's storage number to its canonical bucket
// size; sellers write both marketing sizes ("500 GB") and real ones
// ("512 GB"). Sizes ≤ 64 GB are absent on purpose: on a MacBook they are
// more likely RAM.
var macStorageSizes = map[int]int{
	120: 128, 121: 128, 128: 128,
	240: 256, 250: 256, 256: 256,
	480: 512, 500: 512, 512: 512,
	1000: 1024, 1024: 1024,
	2000: 2048, 2048: 2048,
}

// macModelFromTitle derives a canonical MacBook bucket name, or "" when the
// title doesn't pin the machine down. proc is the lowercased
// computer_processor_model facet ("apple", "intel" or "").
func macModelFromTitle(title, proc string) string {
	t := strings.ToLower(title)
	for _, q := range []string{"”", "″", "’’", `"`, "''", "-inch", "inches", "inch", "-tums", "-tum"} {
		t = strings.ReplaceAll(t, q, " tum ")
	}
	t = macModelCodeRe.ReplaceAllString(t, " ")
	t = strings.Join(strings.Fields(t), " ")

	air := strings.Contains(t, "macbook air")
	pro := strings.Contains(t, "macbook pro")
	if air == pro {
		// A bare "MacBook" is as often a lazy Air/Pro title as a real
		// 12" MacBook; both-lines titles are bundles. Skip either way.
		return ""
	}
	line := "Air"
	if pro {
		line = "Pro"
	}

	// Scan every chip mention: "MacBook Pro 14 M1 2021 ... Apple M1 Pro"
	// names the chip twice and only the later mention carries the tier.
	chip, tier := "", ""
	for _, m := range macChipRe.FindAllStringSubmatch(t, -1) {
		if chip != "" && chip != "M"+m[1] {
			return "" // two different chips: a bundle or a comparison
		}
		chip = "M" + m[1]
		if m[2] != "" {
			if tier != "" && tier != m[2] {
				return "" // "M1 Pro eller M1 Max"
			}
			tier = m[2]
		}
	}
	if chip != "" && proc == "intel" {
		return "" // title and processor facet disagree — trust neither
	}
	size := 0
	if m := macSizeUnitRe.FindStringSubmatch(t); m != nil {
		size, _ = strconv.Atoi(m[1])
	} else if m := macNumAfterLineRe.FindStringSubmatch(t); m != nil && m[2] == "" {
		size, _ = strconv.Atoi(m[1])
	}
	year := 0
	if m := macYearRe.FindStringSubmatch(t); m != nil {
		year, _ = strconv.Atoi(m[1])
	}

	if chip != "" {
		return macSilicon(line, chip, tier, size, year)
	}

	// Intel era: the year is the model. 2020 spans both Intel and M1, so
	// without the processor facet saying Intel it stays ambiguous; from
	// 2021 a missing chip means an unknown tier, not an Intel machine.
	if year < 2006 || year > 2020 || (year == 2020 && proc != "intel") {
		return ""
	}
	validSize := map[string]map[int]bool{
		"Air": {11: true, 13: true},
		"Pro": {13: true, 15: true, 16: true, 17: true},
	}
	if !validSize[line][size] {
		return ""
	}
	return fmt.Sprintf("MacBook %s %d (%d)", line, size, year)
}

func macSilicon(line, chip, tier string, size, year int) string {
	gen := int(chip[1] - '0')
	if line == "Air" {
		if tier != "" {
			return "" // Airs only take plain chips
		}
		if size == 0 {
			switch {
			case gen == 1:
				size = 13 // the M1 Air came in one size
			case chip == "M2" && year == 2022:
				size = 13 // the 15" M2 arrived in 2023
			default:
				return "" // two sizes exist; the bucket needs one
			}
		}
		if size != 13 && size != 15 {
			return ""
		}
		return fmt.Sprintf("MacBook Air %d %s", size, chip)
	}

	switch tier {
	case "":
		// Plain chips shipped in exactly one Pro size: 13" (M1/M2) or
		// 14" (M3/M4). A 16" with a plain chip is a seller dropping the
		// tier — which tier is unknowable, so skip.
		want := 13
		if gen >= 3 {
			want = 14
		}
		if size == 0 {
			size = want
		}
		if size != want {
			return ""
		}
	case "pro", "max":
		if size != 14 && size != 16 {
			return "" // tier chips came in both sizes; the bucket needs one
		}
		chip += " " + strings.ToUpper(tier[:1]) + tier[1:]
	default:
		return "" // no MacBook has an Ultra
	}
	return fmt.Sprintf("MacBook Pro %d %s", size, chip)
}

// macStorageFromTitle finds the SSD size among a title's GB/TB numbers.
// Explicit context ("512 GB SSD", "16 GB RAM") wins; otherwise two bare
// sizes read as RAM+storage ("16GB/512GB") and a single bare size counts
// only when it can't plausibly be RAM. conflict=true means two different
// storage claims (bundles).
func macStorageFromTitle(title string) (gb int, conflict bool) {
	t := strings.ToLower(title)
	var ssd, unmarked []int
	for _, m := range macMemRe.FindAllStringSubmatchIndex(t, -1) {
		n, _ := strconv.Atoi(t[m[2]:m[3]])
		if t[m[4]:m[5]] == "tb" {
			n *= 1024
		}
		after := t[m[1]:min(m[1]+10, len(t))]
		before := t[max(0, m[0]-8):m[0]]
		switch {
		case hasAny(after, "ssd", "lagring", "hårddisk", "hdd", "disk", "nvme") || hasAny(before, "ssd", "hdd", "disk", "lagring"):
			if !slices.Contains(ssd, n) {
				ssd = append(ssd, n)
			}
		// No look-behind for "ram": in "24 GB RAM 1TB" it would swallow
		// the 1TB. "RAM: 16GB" still resolves — a lone 16 is never
		// storage, and next to a real size the smaller number reads as RAM.
		case hasAny(after, "ram", "arbetsminne", "unified"):
			// RAM — never part of the bucket
		default:
			if !slices.Contains(unmarked, n) {
				unmarked = append(unmarked, n)
			}
		}
	}
	if len(ssd) > 1 {
		return 0, true
	}
	if len(ssd) == 1 {
		return macStorageSizes[ssd[0]], false // 0 for odd sizes (160 GB HDDs)
	}
	switch len(unmarked) {
	case 1:
		return macStorageSizes[unmarked[0]], false
	case 2:
		lo, hi := min(unmarked[0], unmarked[1]), max(unmarked[0], unmarked[1])
		if macRAMSizes[lo] {
			return macStorageSizes[hi], false
		}
		return 0, true // two storage-sized numbers: a bundle
	default:
		return 0, len(unmarked) > 2
	}
}

func hasAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
