package quality

import "testing"

func scoreOf(v int) *int { return &v }

// TestBandsNameShippedThresholds pins the band each score falls in under the
// shipped thresholds. These are the boundaries the product was designed around,
// and every platform IP-tier filter and plan IP tier means them, so a change
// here changes what an existing deployment serves.
func TestBandsNameShippedThresholds(t *testing.T) {
	bands := DefaultBands()
	cases := []struct {
		score int
		want  string
	}{
		{100, BandExcellent},
		{95, BandExcellent},
		{94, BandClean},
		{90, BandClean},
		{89, BandFair},
		{80, BandFair},
		{79, BandMixed},
		{60, BandMixed},
		{59, BandPoor},
		{0, BandPoor},
	}
	for _, tc := range cases {
		if got := bands.Name(scoreOf(tc.score)); got != tc.want {
			t.Errorf("score %d: band = %q, want %q", tc.score, got, tc.want)
		}
	}
}

// TestBandsNameConfiguredThresholds proves the setting actually moves the
// boundaries: the same score lands in a different band once an operator changes
// the thresholds, which is the whole point of making them configurable.
func TestBandsNameConfiguredThresholds(t *testing.T) {
	configured := Bands{Excellent: 90, Clean: 80, Fair: 70, Mixed: 50}
	cases := []struct {
		score int
		want  string
	}{
		{90, BandExcellent},
		{89, BandClean},
		{75, BandFair},
		{69, BandMixed},
		{49, BandPoor},
	}
	for _, tc := range cases {
		if got := configured.Name(scoreOf(tc.score)); got != tc.want {
			t.Errorf("score %d: band = %q, want %q", tc.score, got, tc.want)
		}
	}
}

// TestBandsNameUnknownInput covers the two answers that are not a band of a
// real score: no score at all, and a score outside 0-100.
func TestBandsNameUnknownInput(t *testing.T) {
	bands := DefaultBands()
	if got := bands.Name(nil); got != BandUnknown {
		t.Errorf("nil score: band = %q, want %q", got, BandUnknown)
	}
	for _, out := range []int{-1, 101} {
		if got := bands.Name(scoreOf(out)); got != BandUnknown {
			t.Errorf("score %d: band = %q, want %q", out, got, BandUnknown)
		}
	}
}

// TestBandsNormalizedRefusesUnusableSets pins the two inputs that fall back to
// the shipped thresholds rather than being honoured.
//
// An all-zero set is what a config row written before the setting existed
// decodes to, and a set that does not descend would name a high score worse
// than a lower one - both would change what every existing filter means,
// silently.
func TestBandsNormalizedRefusesUnusableSets(t *testing.T) {
	unusable := []Bands{
		{},
		{Excellent: 50, Clean: 60, Fair: 70, Mixed: 80},
		{Excellent: 90, Clean: 90, Fair: 80, Mixed: 60},
		{Excellent: 101, Clean: 90, Fair: 80, Mixed: 60},
		{Excellent: 95, Clean: 90, Fair: 80, Mixed: 0},
	}
	for _, bands := range unusable {
		if got := bands.Normalized(); got != DefaultBands() {
			t.Errorf("Normalized(%+v) = %+v, want the shipped bands", bands, got)
		}
	}

	configured := Bands{Excellent: 90, Clean: 80, Fair: 70, Mixed: 50}
	if got := configured.Normalized(); got != configured {
		t.Errorf("Normalized(%+v) = %+v, want it unchanged", configured, got)
	}
}

// TestPurityBandUsesShippedThresholds keeps the compatibility entry point
// honest: it is the shipped bands, not something else.
func TestPurityBandUsesShippedThresholds(t *testing.T) {
	if got := PurityBand(scoreOf(94)); got != BandClean {
		t.Errorf("PurityBand(94) = %q, want %q", got, BandClean)
	}
	if got := PurityBand(nil); got != BandUnknown {
		t.Errorf("PurityBand(nil) = %q, want %q", got, BandUnknown)
	}
}
