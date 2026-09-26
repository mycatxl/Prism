package publicsource

// Regression tests for the node-target filter.
//
// These exist because the first version of this filter only looked at the
// document's TOP-LEVEL "server" key and accepted the document unconditionally
// when that key was absent. A node can be a chain envelope
// ({"prism_node":1,"main":{...},"deps":[{...}]}) or an endpoints wrapper, and in
// those forms the real target lives inside main/deps — so every envelope-shaped
// node bypassed the filter and a public gist could inject a node pointing at
// loopback, a private range or a cloud metadata address.

import "testing"

func TestValidRawOptionsRejectsNonPublicTargets(t *testing.T) {
	cases := []struct{ name, raw string }{
		// Plain outbound shapes (the original cases keep working).
		{"loopback ipv4", `{"type":"http","server":"127.0.0.1","server_port":8080}`},
		{"private ipv4", `{"type":"http","server":"10.0.0.1","server_port":80}`},
		{"link-local metadata", `{"type":"http","server":"169.254.169.254","server_port":80}`},
		{"carrier-grade nat", `{"type":"socks","server":"100.64.1.1","server_port":1080}`},
		{"alibaba metadata", `{"type":"socks","server":"100.100.100.200","server_port":1080}`},
		{"nat64 of loopback", `{"type":"socks","server":"64:ff9b::7f00:1","server_port":1080}`},
		{"ipv6 loopback", `{"type":"socks","server":"::1","server_port":1080}`},
		{"ipv6 unique local", `{"type":"socks","server":"fd00:ec2::254","server_port":1080}`},
		// inet_aton spellings that net.ParseIP does not recognise.
		{"integer form", `{"type":"socks","server":"2130706433","server_port":1080}`},
		{"short form", `{"type":"socks","server":"127.1","server_port":1080}`},
		{"hex form", `{"type":"socks","server":"0x7f.0.0.1","server_port":1080}`},
		// Names that resolve through local search domains rather than the public DNS.
		{"single label", `{"type":"socks","server":"gateway","server_port":1080}`},
		{"localhost name", `{"type":"socks","server":"localhost","server_port":1080}`},
		{"mdns name", `{"type":"socks","server":"printer.local","server_port":1080}`},
		{"internal name", `{"type":"socks","server":"vault.internal","server_port":1080}`},
		// Envelope forms: the target is not at the top level.
		{"chain main loopback", `{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"socks","server":"127.0.0.1","server_port":1080},"deps":[{"type":"socks","server":"8.8.8.8","server_port":1080}]}`},
		{"chain dep private", `{"prism_node":1,"engine":"singbox","kind":"chain","main":{"type":"socks","server":"93.184.216.35","server_port":1080},"deps":[{"type":"socks","server":"10.1.2.3","server_port":1080}]}`},
		// Shape failures. A server value that is not a usable host string.
		{"server is not a string", `{"type":"http","server":80}`},
		{"server is empty", `{"type":"http","server":"","server_port":80}`},
		{"not json", `not json`},
		{"server inside array", `{"outbounds":[{"type":"socks","server":"127.0.0.1","server_port":1080}]}`},
		{"endpoints wrapper metadata", `{"endpoints":[{"type":"wireguard","tag":"e","server":"169.254.169.254"}]}`},
		{"nested three levels", `{"a":{"b":{"c":{"server":"169.254.169.254"}}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if validRawOptions([]byte(tc.raw)) {
				t.Fatalf("validRawOptions(%s) = true; this target must be rejected", tc.raw)
			}
		})
	}
}

func TestValidRawOptionsAcceptsPublicTargets(t *testing.T) {
	cases := []struct{ name, raw string }{
		{"public ipv4", `{"type":"http","server":"93.184.216.35","server_port":8080}`},
		{"public ipv6", `{"type":"http","server":"2001:4860:4860::8888","server_port":443}`},
		{"public hostname", `{"type":"trojan","server":"cdn.example.com","server_port":8443}`},
		{"public hostname with hyphen", `{"type":"trojan","server":"edge-1.example.co.uk","server_port":8443}`},
		{"trailing dot", `{"type":"trojan","server":"cdn.example.com.","server_port":8443}`},
		{"envelope with public targets", `{"prism_node":1,"main":{"type":"socks","server":"93.184.216.35","server_port":1080},"deps":[{"type":"socks","server":"8.8.8.8","server_port":1080}]}`},
		{"endpoint without a server key", `{"type":"wireguard","tag":"e","peers":[{"address":"93.184.216.35","port":51820}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !validRawOptions([]byte(tc.raw)) {
				t.Fatalf("validRawOptions(%s) = false; this is a usable public node", tc.raw)
			}
		})
	}
}

// TestValidHostClassification pins the host classifier directly, including the
// numeric-label rule that catches the inet_aton spellings.
func TestValidHostClassification(t *testing.T) {
	valid := []string{
		"93.184.216.35", "8.8.8.8", "2001:4860:4860::8888", "2606:4700:4700::1111",
		"example.com", "cdn.example.com", "edge-1.example.co.uk", "a.b.c.example.net",
	}
	for _, host := range valid {
		if !validHost(host) {
			t.Errorf("validHost(%q) = false; want true", host)
		}
	}
	invalid := []string{
		"", " ", "127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254",
		"100.64.0.1", "100.100.100.200", "0.0.0.0", "255.255.255.255",
		"::1", "::", "fe80::1", "fd00::1", "64:ff9b::7f00:1", "ff02::1",
		"2130706433", "127.1", "127.0.1", "0x7f.0.0.1", "0177.0.0.1",
		"localhost", "gateway", "printer.local", "vault.internal", "host.lan",
		"host.home.arpa", "host.corp", "host.private",
		"-leading.example.com", "trailing-.example.com", "host..example.com",
		"exa mple.com", "host/example.com", "user@example.com",
	}
	for _, host := range invalid {
		if validHost(host) {
			t.Errorf("validHost(%q) = true; want false", host)
		}
	}
}
