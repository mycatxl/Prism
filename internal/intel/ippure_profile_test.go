package intel

import (
	"database/sql"
	"prism/internal/intel/store"
	"testing"
	"time"
)

func TestPurityOldProfileFailsClosedBeforeMigration(t *testing.T) {
	for _, profile := range []string{"", "prism-purity-v3", "prism-purity-v4"} {
		row := store.Assessment{IP: "8.8.8.8", Profile: profile, State: "valid", Verdict: "favorable", PurityScore: sql.NullInt64{Int64: 100, Valid: true}, ValidUntilNs: time.Now().Add(time.Hour).UnixNano()}
		got := AssessmentLiteFromRow(row)
		if got.ScoreValue() != nil || got.Verdict == VerdictFavorable {
			t.Errorf("legacy %q usable before recomputation", profile)
		}
	}
}
