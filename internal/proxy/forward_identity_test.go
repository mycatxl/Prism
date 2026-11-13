package proxy

import (
	"net/http/httptest"
	"testing"
)

func TestForwardStripsIdentityWithoutMutatingCaller(t *testing.T) {
	in := httptest.NewRequest("GET", "http://example.com", nil)
	for _, name := range forwardingIdentityHeaders {
		in.Header.Set(name, "fixture-identity")
	}
	in.Header.Set("Accept", "application/json")
	out := prepareForwardOutboundRequest(in)
	for _, name := range forwardingIdentityHeaders {
		if out.Header.Get(name) != "" {
			t.Fatal("identity forwarded", name)
		}
		if in.Header.Get(name) == "" {
			t.Fatal("caller header mutated", name)
		}
	}
	if out.Header.Get("Accept") != "application/json" {
		t.Fatal("unrelated application header removed")
	}
}
