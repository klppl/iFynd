// Package analyze computes reference prices per (model, storage) bucket and
// flags active listings priced well below them. Tradera history contains junk
// prices (1 kr auctions, wishful 3x listings), so the reference metric is a
// median or trimmed mean, never a raw mean.
package analyze

import (
	"fmt"
	"sort"
)

type Metric string

const (
	Median      Metric = "median"
	TrimmedMean Metric = "trimmed_mean"
)

// Reference computes the reference price for a bucket's sold prices.
// trimPct only applies to TrimmedMean and is the fraction (in percent)
// cut from each end. Returns 0 for an empty slice.
func Reference(prices []float64, metric Metric, trimPct float64) float64 {
	if len(prices) == 0 {
		return 0
	}
	sorted := append([]float64(nil), prices...)
	sort.Float64s(sorted)
	switch metric {
	case TrimmedMean:
		cut := int(float64(len(sorted)) * trimPct / 100)
		trimmed := sorted[cut : len(sorted)-cut]
		if len(trimmed) == 0 {
			trimmed = sorted
		}
		var sum float64
		for _, p := range trimmed {
			sum += p
		}
		return sum / float64(len(trimmed))
	default: // Median
		n := len(sorted)
		if n%2 == 1 {
			return sorted[n/2]
		}
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
}

func ParseMetric(s string) (Metric, error) {
	switch Metric(s) {
	case Median, TrimmedMean:
		return Metric(s), nil
	}
	return "", fmt.Errorf("unknown metric %q (want median or trimmed_mean)", s)
}

// PctBelow returns how many percent price sits below ref (positive = cheaper).
func PctBelow(price int, ref float64) float64 {
	if ref <= 0 {
		return 0
	}
	return (1 - float64(price)/ref) * 100
}

// IsHit reports whether an active price undercuts the reference by at least
// thresholdPct.
func IsHit(price int, ref float64, thresholdPct float64) bool {
	return price > 0 && ref > 0 && PctBelow(price, ref) >= thresholdPct
}
