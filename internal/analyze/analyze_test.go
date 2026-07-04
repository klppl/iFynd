package analyze

import (
	"math"
	"testing"
)

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMedian(t *testing.T) {
	if got := Reference([]float64{5000, 1, 4000, 9999, 4500}, Median, 0); !almost(got, 4500) {
		t.Errorf("odd median = %v, want 4500", got)
	}
	if got := Reference([]float64{4000, 5000}, Median, 0); !almost(got, 4500) {
		t.Errorf("even median = %v, want 4500", got)
	}
	if got := Reference(nil, Median, 0); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
}

func TestTrimmedMean(t *testing.T) {
	// 10 values: 10% trim drops the 1 and the 100000.
	prices := []float64{1, 4000, 4100, 4200, 4300, 4400, 4500, 4600, 4700, 100000}
	want := (4000.0 + 4100 + 4200 + 4300 + 4400 + 4500 + 4600 + 4700) / 8
	if got := Reference(prices, TrimmedMean, 10); !almost(got, want) {
		t.Errorf("trimmed mean = %v, want %v", got, want)
	}
	// Tiny slice: trim would remove everything → falls back to full mean.
	if got := Reference([]float64{100}, TrimmedMean, 50); !almost(got, 100) {
		t.Errorf("single = %v, want 100", got)
	}
}

func TestHit(t *testing.T) {
	if !IsHit(3400, 4000, 15) { // exactly 15% below
		t.Error("3400 vs 4000 at 15%% should hit")
	}
	if IsHit(3500, 4000, 15) { // only 12.5% below
		t.Error("3500 vs 4000 at 15%% should not hit")
	}
	if IsHit(0, 4000, 15) || IsHit(3400, 0, 15) {
		t.Error("zero price or zero ref must never hit")
	}
	if got := PctBelow(3000, 4000); !almost(got, 25) {
		t.Errorf("PctBelow = %v, want 25", got)
	}
}
