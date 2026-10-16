package config

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseBlockedDestinationPorts pins the parser of
// PRISM_BLOCKED_DESTINATION_PORTS: the shipped default spelling, an explicit
// override, the empty value, duplicate entries and the tokens that must refuse
// the whole setting while naming themselves.
func TestParseBlockedDestinationPorts(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []int
		wantErr []string // every substring the error must carry
	}{
		{name: "shipped default", raw: defaultBlockedDestinationPortsRaw, want: []int{25, 465, 587}},
		{name: "explicit override", raw: "2525,10025", want: []int{2525, 10025}},
		{name: "empty value blocks nothing", raw: "", want: []int{}},
		{name: "whitespace only blocks nothing", raw: "  \t ", want: []int{}},
		{name: "duplicates are deduplicated and sorted", raw: "587,25,465,25", want: []int{25, 465, 587}},
		{name: "single port", raw: "25", want: []int{25}},
		{name: "non-numeric token", raw: "25,smtp", wantErr: []string{blockedDestinationPortsEnv, "smtp"}},
		{name: "zero port", raw: "0", wantErr: []string{blockedDestinationPortsEnv, `"0"`}},
		{name: "port above the range", raw: "65536", wantErr: []string{blockedDestinationPortsEnv, `"65536"`}},
		{name: "negative port", raw: "-25", wantErr: []string{blockedDestinationPortsEnv, `"-25"`}},
		{
			name:    "bad token refuses the whole list",
			raw:     "25,465,notaport,587",
			wantErr: []string{blockedDestinationPortsEnv, "notaport"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBlockedDestinationPorts(tc.raw)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("parseBlockedDestinationPorts(%q) err = nil, want an error naming %v", tc.raw, tc.wantErr)
				}
				for _, want := range tc.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBlockedDestinationPorts(%q) err = %v, want nil", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseBlockedDestinationPorts(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestLoadEnvConfig_BlockedDestinationPortsDefault pins the effective default:
// an unset variable blocks the SMTP ports on every local direct dial.
func TestLoadEnvConfig_BlockedDestinationPortsDefault(t *testing.T) {
	unsetEnvForConfigTest(t, blockedDestinationPortsEnv)
	setEnvs(t, requiredEnvs())

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg.BlockedDestinationPorts, []int{25, 465, 587}) {
		t.Fatalf("BlockedDestinationPorts = %v, want [25 465 587]", cfg.BlockedDestinationPorts)
	}
}

// TestLoadEnvConfig_BlockedDestinationPortsEmptyDisables pins that an explicit
// empty value means "block no port" rather than "keep the default".
func TestLoadEnvConfig_BlockedDestinationPortsEmptyDisables(t *testing.T) {
	envs := requiredEnvs()
	envs[blockedDestinationPortsEnv] = ""
	setEnvs(t, envs)

	cfg, err := LoadEnvConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.BlockedDestinationPorts) != 0 {
		t.Fatalf("BlockedDestinationPorts = %v, want an empty list", cfg.BlockedDestinationPorts)
	}
}

// TestLoadEnvConfig_BlockedDestinationPortsInvalidValue pins that one bad token
// fails the whole configuration and the error names the variable and that
// token.
func TestLoadEnvConfig_BlockedDestinationPortsInvalidValue(t *testing.T) {
	envs := requiredEnvs()
	envs[blockedDestinationPortsEnv] = "25,smtp"
	setEnvs(t, envs)

	_, err := LoadEnvConfig()
	if err == nil {
		t.Fatal("expected an error for an invalid PRISM_BLOCKED_DESTINATION_PORTS")
	}
	assertContains(t, err.Error(), blockedDestinationPortsEnv)
	assertContains(t, err.Error(), "smtp")
}
