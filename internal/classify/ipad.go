package classify

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// iPad listings on Tradera carry no structured model attribute (only
// tablet_brand), so classification is title-only. The bucket needs family
// (iPad / Air / mini / Pro), generation, and for Pro/modern Air the screen
// size — sellers express these interchangeably as generation ("3:e gen"),
// chip ("M1", "A16"), year ("2021"), or Apple's marketing name. Everything
// here normalizes to one canonical bucket per device; when the title doesn't
// pin the device down (e.g. "iPad Pro 12.9" — six generations), we skip.

var (
	// "3:e gen", "10e generationen", "7th generation", "6 generation"
	ipadGenBeforeRe = regexp.MustCompile(`\b(\d{1,2})\s*(?::e|:a|e|a|th|st|nd|rd)?[\s.]*gen(?:eration(?:en)?)?\b`)
	// "gen4", "gen. 6", "generation 2"
	ipadGenAfterRe = regexp.MustCompile(`\bgen(?:eration(?:en)?)?\.?\s*(\d{1,2})\b`)
	// "(sjunde generationen)"
	ipadGenWordRe = regexp.MustCompile(`\b(första|andra|tredje|fjärde|femte|sjätte|sjunde|åttonde|nionde|tionde|elfte)\s+generationen`)
	ipadChipRe    = regexp.MustCompile(`\b(m[1-4]|a16|a17pro)\b`)
	ipadYearRe    = regexp.MustCompile(`\b(20[0-2]\d)\b`)
	ipadSizeRe    = regexp.MustCompile(`\b(12[.,]9|10[.,]5|9[.,]7|13|11)\b`)
	// Apple hardware model codes ("A2459") — stripped so their digits are
	// never mistaken for a generation.
	ipadModelCodeRe = regexp.MustCompile(`\ba\d{3,4}\b`)
	// first number directly after "ipad"/"air"/"mini"
	ipadNumAfterFamilyRe = regexp.MustCompile(`\b(?:ipad|air|mini)\s*\(?\s*(\d{1,4}(?:[.,]\d)?)\b`)
	ipadMiniRe           = regexp.MustCompile(`\bmini\b`)
	ipadAirRe            = regexp.MustCompile(`\bair\b`)
	ipadProRe            = regexp.MustCompile(`\bpro\b`)
)

var ipadGenWords = map[string]int{
	"första": 1, "andra": 2, "tredje": 3, "fjärde": 4, "femte": 5, "sjätte": 6,
	"sjunde": 7, "åttonde": 8, "nionde": 9, "tionde": 10, "elfte": 11,
}

// Release-year → generation, per family/size. A year only decides when no
// explicit generation or chip is present.
var ipadYearGen = map[string]map[int]int{
	"ipad":    {2010: 1, 2011: 2, 2012: 3, 2014: 4, 2017: 5, 2018: 6, 2019: 7, 2020: 8, 2021: 9, 2022: 10, 2025: 11},
	"air":     {2013: 1, 2014: 2, 2019: 3, 2020: 4, 2022: 5},
	"mini":    {2012: 1, 2013: 2, 2014: 3, 2015: 4, 2019: 5, 2021: 6, 2024: 7},
	"pro11":   {2018: 1, 2020: 2, 2021: 3, 2022: 4},
	"pro12.9": {2015: 1, 2017: 2, 2018: 3, 2020: 4, 2021: 5, 2022: 6},
}

// ipadModelFromTitle derives a canonical iPad bucket name, or "" when the
// title doesn't identify the device confidently.
func ipadModelFromTitle(title string) string {
	t := strings.ToLower(title)
	for _, q := range []string{"”", "″", "\"", "''", "-inch", "-tums", "-tum"} {
		t = strings.ReplaceAll(t, q, " ")
	}
	t = strings.ReplaceAll(t, "a17 pro", "a17pro")
	t = ipadModelCodeRe.ReplaceAllString(t, " ")
	if !strings.Contains(t, "ipad") {
		return ""
	}

	gen := 0
	if m := ipadGenBeforeRe.FindStringSubmatch(t); m != nil {
		gen, _ = strconv.Atoi(m[1])
	} else if m := ipadGenAfterRe.FindStringSubmatch(t); m != nil {
		gen, _ = strconv.Atoi(m[1])
	} else if m := ipadGenWordRe.FindStringSubmatch(t); m != nil {
		gen = ipadGenWords[m[1]]
	}
	chip := ""
	if m := ipadChipRe.FindStringSubmatch(t); m != nil {
		chip = m[1]
	}
	year := 0
	if m := ipadYearRe.FindStringSubmatch(t); m != nil {
		year, _ = strconv.Atoi(m[1])
	}
	size := ""
	if m := ipadSizeRe.FindStringSubmatch(t); m != nil {
		size = strings.ReplaceAll(m[1], ",", ".")
	}
	// First number right after the family word: a generation for base/Air/
	// mini ("iPad 9", "Air 2"), never for Pro (there it's the screen size,
	// which ipadSizeRe already captured).
	famNum := 0.0
	if m := ipadNumAfterFamilyRe.FindStringSubmatch(t); m != nil {
		famNum, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	}

	switch {
	case ipadMiniRe.MatchString(t):
		return ipadMini(gen, chip, year, famNum)
	case ipadAirRe.MatchString(t):
		return ipadAir(gen, chip, year, size, famNum)
	case ipadProRe.MatchString(t):
		return ipadPro(gen, chip, year, size)
	default:
		return ipadBase(gen, chip, year, famNum)
	}
}

func ipadBase(gen int, chip string, year int, famNum float64) string {
	if gen == 0 && famNum == float64(int(famNum)) && famNum >= 2 && famNum <= 11 {
		gen = int(famNum) // "iPad 9" (screen sizes like 10.2 are non-integer)
	}
	if gen == 0 && chip == "a16" {
		gen = 11
	}
	if gen == 0 {
		gen = ipadYearGen["ipad"][year]
	}
	if gen < 2 || gen > 11 {
		return ""
	}
	return fmt.Sprintf("iPad %d", gen)
}

func ipadMini(gen int, chip string, year int, famNum float64) string {
	if gen == 0 && famNum == float64(int(famNum)) && famNum >= 1 && famNum <= 7 {
		gen = int(famNum)
	}
	if gen == 0 && chip == "a17pro" {
		gen = 7
	}
	if gen == 0 {
		gen = ipadYearGen["mini"][year]
	}
	if gen < 1 || gen > 7 {
		return ""
	}
	return fmt.Sprintf("iPad mini %d", gen)
}

func ipadAir(gen int, chip string, year int, size string, famNum float64) string {
	if gen == 0 && famNum >= 1 && famNum <= 5 && famNum == float64(int(famNum)) {
		gen = int(famNum)
	}
	if gen == 0 {
		// The 2024/2025 Airs are named by chip; sellers often write the
		// year instead.
		if chip == "" && (year == 2024 || year == 2025) {
			chip = map[int]string{2024: "m2", 2025: "m3"}[year]
		}
		switch {
		case chip == "m1":
			gen = 5
		case size == "10.5":
			gen = 3 // only Air with that panel
		case chip == "m2" || chip == "m3" || chip == "m4":
			// Modern Airs come in two sizes; without one the bucket is ambiguous.
			if size != "11" && size != "13" {
				return ""
			}
			return fmt.Sprintf("iPad Air %s (%s)", size, strings.ToUpper(chip))
		default:
			gen = ipadYearGen["air"][year]
		}
	}
	if gen < 1 || gen > 5 {
		return ""
	}
	return fmt.Sprintf("iPad Air %d", gen)
}

func ipadPro(gen int, chip string, year int, size string) string {
	switch size {
	case "9.7", "10.5":
		return "iPad Pro " + size // single-generation panels
	case "13":
		return "iPad Pro 13 (M4)" // only M4 exists
	case "11", "12.9":
	default:
		return "" // Pro without a size is unbucketable
	}
	if gen == 0 {
		switch {
		case chip == "m4":
			if size == "11" {
				return "iPad Pro 11 (M4)"
			}
			return "" // no 12.9" M4
		case chip == "m1":
			gen = map[string]int{"11": 3, "12.9": 5}[size]
		case chip == "m2":
			gen = map[string]int{"11": 4, "12.9": 6}[size]
		default:
			gen = ipadYearGen["pro"+size][year]
		}
	}
	maxGen := map[string]int{"11": 4, "12.9": 6}[size]
	if gen < 1 || gen > maxGen {
		return ""
	}
	return fmt.Sprintf("iPad Pro %s (gen %d)", size, gen)
}
