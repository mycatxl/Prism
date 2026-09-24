package publicsource

import (
	"strings"
	"testing"
)

func TestPresetURLsAreHTTPSAndUnique(t *testing.T) {
	global := make(map[string]string)
	for group, urls := range PresetURLsByGroup() {
		if len(urls) == 0 {
			t.Fatalf("preset %q is empty", group)
		}
		local := make(map[string]struct{}, len(urls))
		for _, url := range urls {
			if !strings.HasPrefix(url, "https://") {
				t.Fatalf("preset %q has a non-HTTPS URL: %q", group, url)
			}
			if _, dup := local[url]; dup {
				t.Fatalf("preset %q lists %q twice", group, url)
			}
			local[url] = struct{}{}
			if owner, dup := global[url]; dup && owner != PresetClassic && group != PresetClassic {
				t.Fatalf("%q is listed in both %q and %q", url, owner, group)
			}
			if _, dup := global[url]; !dup {
				global[url] = group
			}
		}
	}
}

func TestPresetURLsExpandsAll(t *testing.T) {
	all, err := PresetURLs([]string{PresetAll})
	if err != nil {
		t.Fatalf("PresetURLs(all) error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("PresetURLs(all) returned nothing")
	}

	// Every group URL must appear exactly once in the expansion.
	seen := make(map[string]int, len(all))
	for _, url := range all {
		seen[url]++
	}
	for group, urls := range PresetURLsByGroup() {
		for _, url := range urls {
			if seen[url] != 1 {
				t.Fatalf("group %q URL %q appears %d times in the all expansion", group, url, seen[url])
			}
		}
	}
	if len(all) != len(seen) {
		t.Fatalf("PresetURLs(all) has duplicates: %d entries, %d unique", len(all), len(seen))
	}
}

func TestPresetURLsSelectsSingleGroup(t *testing.T) {
	nodes, err := PresetURLs([]string{PresetNodes})
	if err != nil {
		t.Fatalf("PresetURLs(nodes) error = %v", err)
	}
	want := PresetURLsByGroup()[PresetNodes]
	if len(nodes) != len(want) {
		t.Fatalf("PresetURLs(nodes) = %d URLs, want %d", len(nodes), len(want))
	}
	for i, url := range want {
		if nodes[i] != url {
			t.Fatalf("PresetURLs(nodes)[%d] = %q, want %q", i, nodes[i], url)
		}
	}
}

func TestPresetURLsIsCaseInsensitiveAndOrdered(t *testing.T) {
	got, err := PresetURLs([]string{"  NODES  ", "Socks"})
	if err != nil {
		t.Fatalf("PresetURLs error = %v", err)
	}
	want, err := PresetURLs([]string{PresetNodes, PresetSOCKS})
	if err != nil {
		t.Fatalf("PresetURLs error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d URLs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPresetURLsRejectsUnknownName(t *testing.T) {
	if _, err := PresetURLs([]string{"deffo-not-a-preset"}); err == nil {
		t.Fatal("PresetURLs must reject an unknown preset name")
	}
}

func TestPresetNoneSelectsNothing(t *testing.T) {
	urls, err := PresetURLs([]string{PresetNone})
	if err != nil {
		t.Fatalf("PresetURLs(none) error = %v", err)
	}
	if len(urls) != 0 {
		t.Fatalf("PresetURLs(none) = %d URLs, want 0", len(urls))
	}
	if _, err := PresetURLs([]string{PresetNone, PresetNodes}); err == nil {
		t.Fatal("PresetURLs must reject combining none with another preset")
	}
}

func TestPresetURLsEmptySelection(t *testing.T) {
	urls, err := PresetURLs(nil)
	if err != nil {
		t.Fatalf("PresetURLs(nil) error = %v", err)
	}
	if len(urls) != 0 {
		t.Fatalf("PresetURLs(nil) = %d URLs, want 0", len(urls))
	}
}

func TestDefaultPresetNamesPreservesHistoricalSource(t *testing.T) {
	urls, err := PresetURLs(DefaultPresetNames())
	if err != nil {
		t.Fatalf("PresetURLs(DefaultPresetNames()) error = %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("default presets expand to %d URLs, want 1", len(urls))
	}
	if !strings.Contains(urls[0], "TheSpeedX/PROXY-List") {
		t.Fatalf("default preset is %q, want the TheSpeedX list", urls[0])
	}
}

func TestPresetNamesAndDescriptionsCoverEveryGroup(t *testing.T) {
	names := PresetNames()
	for _, wanted := range []string{PresetAll, PresetNone, PresetClassic, PresetHTTP, PresetSOCKS, PresetNodes} {
		found := false
		for _, name := range names {
			if name == wanted {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("PresetNames() is missing %q: %v", wanted, names)
		}
	}
	descriptions := PresetDescriptions()
	if len(descriptions) != len(names) {
		t.Fatalf("PresetDescriptions() has %d lines, PresetNames() has %d", len(descriptions), len(names))
	}
}
