package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// loaderEnvLiteral matches the PRISM_ variable names the configuration loader
// spells out: every `"PRISM_..."` string literal in internal/config/env.go and
// quality.go is a variable the process reads (a helper call, a lookup, a
// validation name or the blocked-ports constant).
var loaderEnvLiteral = regexp.MustCompile(`"(PRISM_[A-Z0-9_]+)"`)

// TestCheckConfigCoversEveryLoaderVariable pins `prism check-config` and
// .env.example to the loader.
//
// The report is how an operator answers "why does this deployment behave this
// way", so a setting the loader reads but the report omits reads as "this build
// has no such setting" rather than "it is at its default". The same omission in
// .env.example means the setting is undocumented. Scanning the loader instead
// of listing the names a second time is what keeps the three in step when a
// variable is added.
func TestCheckConfigCoversEveryLoaderVariable(t *testing.T) {
	names := loaderEnvVariables(t)
	// A scan that silently stops matching would make every assertion below
	// vacuous, so the lower bound is a guard, not a count to maintain: the
	// loader reads 64 variables today.
	if len(names) < 60 {
		t.Fatalf("scanned %d PRISM_ variables from the loader, want at least 60: %v", len(names), names)
	}

	// The loader refuses to start without a usable proxy token, and the report
	// is what is under test here, so pin a valid one. The empty egress target
	// exercises the "set but empty" source the report has to distinguish from
	// the default.
	t.Setenv("PRISM_PROXY_TOKEN", "0123456789abcdef0123456789abcdef")
	t.Setenv("PRISM_EGRESS_TRACE_URL", "")
	// Unset is not the same answer as "set but empty": the legend separates
	// them, and a report that says "empty" about a variable the deployment
	// never mentions sends the operator looking for a line that is not there.
	// Two readers record the source themselves - envBool, a Prism-only switch
	// read straight from the environment, and qualityEnv - so pin both by
	// removing the line for the duration of the test. t.Setenv records the
	// original value for the cleanup; the Unsetenv right after it is what the
	// report then has to read as "not set".
	for _, name := range []string{"PRISM_ENFORCE_STRONG_TOKENS", "PRISM_QUALITY_WORKERS"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}

	var stdout, stderr bytes.Buffer
	if err := runCheckConfigCommand(nil, &stdout, &stderr); err != nil {
		t.Fatalf("check-config: %v", err)
	}
	out := stdout.String()

	// The source column is part of the contract, not decoration: a variable
	// pinned by the environment says so, and one that is set but empty is not
	// reported as an override.
	for _, want := range []string{
		"PRISM_PROXY_TOKEN=***\t[env]",
		"PRISM_EGRESS_TRACE_URL=(empty)\t[empty]",
		"PRISM_ENFORCE_STRONG_TOKENS=true\t[default]",
		"PRISM_QUALITY_WORKERS=2\t[default]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check-config output is missing %q", want)
		}
	}

	rawExample, err := os.ReadFile(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	example := string(rawExample)

	for _, name := range names {
		if !strings.Contains(out, name+"=") {
			t.Errorf("check-config does not report %s (the loader reads it)", name)
		}
		if !envExampleDocuments(example, name) {
			t.Errorf(".env.example does not document %s (the loader reads it)", name)
		}
	}
}

// loaderEnvVariables returns the PRISM_ variable names the loader reads,
// sorted, scanned from the configuration package.
func loaderEnvVariables(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, name := range []string{"env.go", "quality.go"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "config", name))
		if err != nil {
			t.Fatalf("read internal/config/%s: %v", name, err)
		}
		for _, match := range loaderEnvLiteral.FindAllStringSubmatch(string(raw), -1) {
			seen[match[1]] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// envExampleDocuments reports whether .env.example carries a line for name,
// either as an active setting or as a commented-out one.
func envExampleDocuments(example, name string) bool {
	for _, line := range strings.Split(example, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if strings.HasPrefix(line, name+"=") {
			return true
		}
	}
	return false
}
