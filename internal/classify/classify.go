// Package classify derives a normalized (model, storage) bucket from a
// Tradera listing. It prefers the structured attributes Tradera embeds in the
// search payload (mobile_model, mobile_disk_memory) and falls back to parsing
// the messy Swedish free-text title. Listings that look like accessories,
// bundles, broken/parts phones, or that can't be confidently bucketed are
// rejected with a reason so they can be audited instead of polluting averages.
package classify

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/klppl/ifynd/internal/tradera"
)

// Family selects which device family's model grammar to apply. iPhone is the
// only family today; the type stays an enum so a new category (e.g. a
// re-added iPad/MacBook, or Watch) is a matter of adding a constant, a
// junkByFamily entry, and a branch in Item.
type Family string

const (
	IPhone Family = "iphone"
)

func ParseFamily(s string) (Family, error) {
	switch Family(s) {
	case IPhone:
		return Family(s), nil
	}
	return "", fmt.Errorf("unknown family %q (want iphone)", s)
}

// Result is a confident classification.
type Result struct {
	Model     string // canonical, e.g. "iPhone 13 Pro" or "iPad Pro 11 (gen 3)"
	StorageGB int    // 1 TB = 1024
}

// brokenWords reject listings that are not a plain working device, in any
// family. Matched against the lowercased title as substrings, so keep
// entries specific (e.g. "batteri till", not "batteri" — titles
// legitimately brag about "100% batterihälsa").
var brokenWords = []string{
	// broken / for parts
	"defekt", "trasig", "reservdel", "för delar", "till delar", "sprucken",
	"spräckt", "krossad", "skadad", "fungerar ej", "fungerar inte",
	"startar ej", "startar inte", "död ", "dead ",
	"cracked", "broken", "faulty", "for parts", "spare part",
	// locked
	"icloud", "operatörslåst", "simlåst", "sim-låst", "låst till",
	// lookalikes ("ser ut som en iPhone 17") sold under the pricier model
	"ser ut som", "liknar", "ser ut att vara",
	// not a device at all
	"extra frakt", "fraktkostnad", "ordernr", "box till", "låda till",
}

// accessoryWords reject accessory/empty-box/bundle listings.
var accessoryWords = []string{
	"skal", "fodral", "case", "hörlur", "airpod", "laddare", "kabel",
	"adapter", "skärmskydd", "pansarglas", "endast kartong", "tom kartong",
	"bara kartong", "kartong till", "box only", "empty box", "skärm till",
	"display till", "batteri till", "moderkort", "attrapp", "dummy",
	"kopia", "replika", "watch", "paket", "stycken", " pack",
}

// multiLotRe catches "3 iPhone", "2 st iPhone" — lots sold as one listing,
// whose single price would poison a per-device bucket. Single digit only, so
// years ("2021 iPhone") never match.
var multiLotRe = regexp.MustCompile(`(?i)\b\d\s*(?:st\.?\s+)?iphone`)

// junkByFamily is the full reject list per family: shared broken/locked words
// plus family-tuned accessory words and the other families' devices. Keyed by
// family so a new category plugs in its own list.
var junkByFamily = map[Family][]string{
	IPhone: slices.Concat(brokenWords, accessoryWords, []string{"ipad", "surfplatta", "macbook"}),
}

// junkPrefixes catch titles that OPEN with a non-phone word where the word
// alone would false-positive ("KARTONG iPhone 14 Pro" is an empty box, but
// "iPhone 14 Pro med originalkartong" is a phone).
var junkPrefixes = []string{"kartong", "box ", "tom "}

var badConditions = []string{"defekt", "reparation"}

// storageRe matches "128GB", "128 GB", "1TB", "1 tb", "128G". The whitelist
// keeps bare-G matches honest ("5G" is a network, not 5 gigabytes).
var storageRe = regexp.MustCompile(`(?i)\b(\d{1,4})\s*(gb|tb|g)\b`)

var validGB = map[int]bool{8: true, 16: true, 32: true, 64: true, 128: true, 256: true, 512: true, 1024: true, 2048: true}

// modelRe: base identifier followed optionally by a variant. Longest
// alternatives first so "16e"/"6s"/"xs" win over "16"/"6"/"x".
var modelRe = regexp.MustCompile(`(?i)\biphone\s*(3gs|3g|4s|5s|5c|6s|16e|17|16|15|14|13|12|11|xs|xr|x|se|4|5|6|7|8)[\s,\-–]*(pro\s*max|promax|pro|plus|mini|max|air|\+)?\b`)

var seYearRe = regexp.MustCompile(`(?i)\bse\b.{0,20}?\b(2016|2020|2022)\b`)

// Item classifies a Tradera listing. When ok is false, reason says why.
func Item(it *tradera.Item, fam Family) (res Result, ok bool, reason string) {
	title := strings.ToLower(it.ShortDescription)

	for _, w := range junkByFamily[fam] {
		if strings.Contains(title, w) {
			return res, false, "junk word: " + w
		}
	}
	for _, p := range junkPrefixes {
		if strings.HasPrefix(title, p) {
			return res, false, "junk prefix: " + p
		}
	}
	if multiLotRe.MatchString(title) {
		return res, false, "multi-device lot"
	}
	if cond := strings.ToLower(it.Attr("condition")); cond != "" {
		for _, bad := range badConditions {
			if strings.Contains(cond, bad) {
				return res, false, "condition: " + cond
			}
		}
	}
	if brand := it.Attr("mobile_brand"); brand != "" && !strings.EqualFold(brand, "apple") {
		return res, false, "brand: " + brand
	}

	// Sellers get the structured attributes wrong ("iPhone 16" attribute on
	// an "iPhone 16 Plus" title). When attribute and title both parse
	// confidently but disagree, trust neither.
	attrModel := modelFromAttr(it)
	titleModel := modelFromTitle(it.ShortDescription)
	if attrModel != "" && titleModel != "" && !strings.EqualFold(attrModel, titleModel) {
		// A bare "iPhone SE" attribute vs "iPhone SE (2020)" from the title
		// is added specificity, not a contradiction.
		if strings.EqualFold(attrModel, "iPhone SE") && strings.HasPrefix(strings.ToLower(titleModel), "iphone se (") {
			attrModel = titleModel
		} else {
			return res, false, fmt.Sprintf("model mismatch: attr=%s title=%s", attrModel, titleModel)
		}
	}
	model := attrModel
	if model == "" {
		model = titleModel
	}
	if model == "" {
		return res, false, "no confident model"
	}

	attrGB := storageFromAttr(it)
	titleGB, conflict := storageFromTitle(title)
	if attrGB != 0 && titleGB != 0 && attrGB != titleGB {
		return res, false, fmt.Sprintf("storage mismatch: attr=%d title=%d", attrGB, titleGB)
	}
	gb := attrGB
	if gb == 0 {
		if conflict {
			return res, false, "ambiguous storage in title"
		}
		gb = titleGB
	}
	if gb == 0 {
		return res, false, "no storage size"
	}

	return Result{Model: model, StorageGB: gb}, true, ""
}

func modelFromAttr(it *tradera.Item) string {
	m := strings.TrimSpace(it.Attr("mobile_model"))
	if m == "" || !strings.HasPrefix(strings.ToLower(m), "iphone") {
		return ""
	}
	if strings.EqualFold(m, "iphone") {
		return "" // bare "iPhone" carries no model information
	}
	// Attribute values are already canonical ("iPhone 14 Pro Max",
	// "iPhone SE (2022)"); just normalize the prefix casing.
	return "iPhone" + m[len("iphone"):]
}

func storageFromAttr(it *tradera.Item) int {
	m := storageRe.FindStringSubmatch(it.Attr("mobile_disk_memory"))
	if m == nil {
		return 0
	}
	return toGB(m[1], m[2])
}

func modelFromTitle(title string) string {
	m := modelRe.FindStringSubmatch(title)
	if m == nil {
		return ""
	}
	base := strings.ToLower(m[1])
	variant := strings.ToLower(strings.Join(strings.Fields(m[2]), " "))

	if base == "se" {
		// SE generations have very different values; without a year the
		// bucket would mix them.
		if y := seYearRe.FindStringSubmatch(title); y != nil {
			return fmt.Sprintf("iPhone SE (%s)", y[1])
		}
		return ""
	}

	switch base {
	case "x", "xr", "xs":
		base = strings.ToUpper(base)
	case "3g", "3gs":
		base = strings.ToUpper(base[:2]) + base[2:]
	}
	name := "iPhone " + base
	switch variant {
	case "pro max", "promax":
		name += " Pro Max"
	case "pro":
		name += " Pro"
	case "plus", "+":
		name += " Plus"
	case "mini":
		name += " mini"
	case "max":
		name += " Max"
	case "air":
		name += " Air"
	}
	return name
}

// storageFromTitle returns the storage in GB, or conflict=true when the title
// names two different plausible sizes (bundles, trade-in lists).
func storageFromTitle(title string) (gb int, conflict bool) {
	for _, m := range storageRe.FindAllStringSubmatch(title, -1) {
		g := toGB(m[1], m[2])
		if g == 0 {
			continue
		}
		if gb != 0 && g != gb {
			return 0, true
		}
		gb = g
	}
	return gb, false
}

func toGB(num, unit string) int {
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0
	}
	if strings.EqualFold(unit, "tb") {
		n *= 1024
	}
	if !validGB[n] {
		return 0
	}
	return n
}
