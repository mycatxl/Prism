package publicsource

import (
	"context"
	"os"
	"testing"
	"time"

	"prism/internal/netutil"
)

// TestPresetSourcesYieldNodes is the guard that keeps the built-in preset list
// honest. Public proxy lists rot constantly: files get deleted, moved, or start
// returning an error page, and a shipped preset that yields nothing is worse
// than no preset at all because it costs a request every cycle and hides the
// failure inside an aggregate.
//
// It is opt-in because it needs the public internet:
//
//	PUBLIC_SOURCE_LIVE_PRESET_TEST=1 go test ./internal/publicsource/ -run PresetSources -v
func TestPresetSourcesYieldNodes(t *testing.T) {
	if os.Getenv("PUBLIC_SOURCE_LIVE_PRESET_TEST") == "" {
		t.Skip("set PUBLIC_SOURCE_LIVE_PRESET_TEST=1 to verify every preset source")
	}

	downloader := netutil.NewDirectDownloader(
		func() time.Duration { return 45 * time.Second },
		func() string { return "Prism/PresetCheck/1.0" },
	)

	groups := PresetURLsByGroup()
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	// Deterministic order so a failure report is reproducible.
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}

	total := 0
	for _, group := range names {
		for _, url := range groups[group] {
			if total >= 64 {
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			body, err := downloader.Download(ctx, url)
			cancel()
			if err != nil {
				t.Errorf("preset %s: %s is unreachable: %v", group, url, err)
				continue
			}
			if len(body) == 0 {
				t.Errorf("preset %s: %s returned an empty body", group, url)
				continue
			}
			// parseSourceBody is the path the collector itself uses, including
			// the format normaliser, so the preset check must use it too.
			parsed, err := parseSourceBody(body)
			if err != nil {
				t.Errorf("preset %s: %s no longer parses: %v", group, url, err)
				continue
			}
			accepted := 0
			for _, item := range parsed {
				if validRawOptions(item.RawOptions) {
					accepted++
				}
			}
			if accepted == 0 {
				t.Errorf("preset %s: %s yielded no usable node", group, url)
				continue
			}
			t.Logf("preset %s: %s -> %d nodes", group, url, accepted)
			total++
		}
	}
	if total == 0 {
		t.Fatal("no preset source produced nodes; the whole preset list is stale")
	}
	t.Logf("verified %d preset sources", total)
}
