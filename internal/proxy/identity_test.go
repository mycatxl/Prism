package proxy

import "testing"

func TestParseV1PlatformAccountIdentity(t *testing.T) {
	tests := []struct {
		identity string
		token    string
		plat     string
		account  string
	}{
		{identity: "", token: "", plat: "", account: ""},
		{identity: ":", token: "", plat: "", account: ""},
		{identity: ".", token: "", plat: "", account: ""},
		{identity: "a", token: "", plat: "a", account: ""},
		{identity: "a:", token: "", plat: "a", account: ""},
		{identity: ":b", token: "", plat: "", account: "b"},
		{identity: ".:", token: "", plat: "", account: ":"},
		{identity: ":.", token: "", plat: "", account: "."},
		{identity: ":..", token: "", plat: "", account: ".."},
		{identity: "a:b", token: "", plat: "a", account: "b"},
	}
	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			plat, account := parseV1PlatformAccountIdentity(tt.identity)
			if plat != tt.plat || account != tt.account {
				t.Fatalf(
					"identity=%q: got plat=%q account=%q, want plat=%q account=%q",
					tt.identity,
					plat,
					account,
					tt.plat,
					tt.account,
				)
			}
		})
	}
}
