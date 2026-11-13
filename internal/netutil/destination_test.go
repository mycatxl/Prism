package netutil

import "testing"

func TestParseDestinationStrictPorts(t *testing.T) {
	for _, authority := range []string{"example.com:65536", "example.com:65561", "example.com:-1", "example.com:+80", "example.com:", "example.com", ":80", "[invalid]:80", "bad\x00host:80"} {
		if destination, err := ParseDestination(authority); err == nil {
			t.Errorf("accepted %q as %v", authority, destination)
		}
	}
	for authority, port := range map[string]uint16{"example.com:80": 80, "example.com:0": 0, "[2001:db8::1]:65535": 65535, "127.0.0.1:443": 443} {
		destination, err := ParseDestination(authority)
		if err != nil || destination.Port != port {
			t.Errorf("%s: %v %v", authority, destination, err)
		}
	}
}
