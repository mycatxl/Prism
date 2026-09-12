package inspection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"prism/internal/quality"
)

const torRegistryURL = "https://onionoo.torproject.org/details?type=relay&running=true&fields=or_addresses,exit_addresses,flags,last_seen"
const torRegistryMaxBody = 4 * 1024 * 1024

const (
	torRelay uint8 = 1 << iota
	torGuard
	torExit
)

type RegistryStatus struct {
	ID        string     `json:"id"`
	Ready     bool       `json:"ready"`
	Entries   int        `json:"entries"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	ErrorCode string     `json:"error_code,omitempty"`
}

// TorRegistry reads a public CC0 dataset without sending inventory addresses.
// First use starts a bounded background refresh; lookups never wait on network.
type TorRegistry struct {
	mu          sync.Mutex
	roles       map[netip.Addr]uint8
	published   time.Time
	checked     time.Time
	nextRefresh time.Time
	modified    string
	refreshing  bool
	errorCode   string
	client      *http.Client
	url         string
}

func NewTorRegistry() *TorRegistry {
	return &TorRegistry{client: NewProxyCheckProvider("", 10*time.Second, time.Hour).client, url: torRegistryURL}
}

func (r *TorRegistry) Source(ip netip.Addr) quality.SourceSummary {
	source := quality.SourceSummary{Provider: "torproject", Configured: true, State: "unobserved"}
	ip, err := quality.PublicIP(ip)
	if err != nil {
		return source
	}
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.refreshing && !now.Before(r.nextRefresh) {
		r.refreshing = true
		r.nextRefresh = now.Add(5 * time.Minute)
		go r.refresh()
	}
	if r.roles == nil {
		return source
	}
	validUntil := r.checked.Add(6 * time.Hour)
	if limit := r.published.Add(6 * time.Hour); limit.Before(validUntil) {
		validUntil = limit
	}
	source.State = "valid"
	if !now.Before(validUntil) {
		source.State = "stale"
	}
	roles := []string{}
	bits := r.roles[ip]
	if bits&torExit != 0 {
		roles = append(roles, "exit")
	}
	if bits&torGuard != 0 {
		roles = append(roles, "guard")
	}
	if bits&torRelay != 0 {
		roles = append(roles, "relay")
	}
	published := r.published
	e := &quality.Evidence{IP: ip.String(), Provider: "torproject", Profile: "onionoo-public-relays-v1",
		IPType: "unknown", SourceType: "Public Tor registry", Grade: "unknown", TorRoles: roles,
		ObservedAt: r.checked, ValidUntil: validUntil, SourceUpdatedAt: &published}
	// An absent public listing cannot rule out a private bridge or Tor use.
	if bits != 0 {
		yes := true
		e.Signals.Tor = &yes
	}
	source.Evidence = e
	return source
}

func (r *TorRegistry) Status() RegistryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := RegistryStatus{ID: "torproject", Ready: r.roles != nil && time.Since(r.published) < 6*time.Hour, Entries: len(r.roles), ErrorCode: r.errorCode}
	if !r.published.IsZero() {
		published := r.published
		status.UpdatedAt = &published
	}
	return status
}

func (r *TorRegistry) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	r.mu.Lock()
	modified := r.modified
	r.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		r.failed("REGISTRY_REQUEST", 5*time.Minute)
		return
	}
	request.Header.Set("User-Agent", "Prism/1.0 (public Tor role classification)")
	request.Header.Set("Accept", "application/json")
	if modified != "" {
		request.Header.Set("If-Modified-Since", modified)
	}
	response, err := r.client.Do(request)
	if err != nil {
		r.failed("REGISTRY_UNAVAILABLE", 5*time.Minute)
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		r.mu.Lock()
		r.refreshing = false
		r.nextRefresh = time.Now().Add(time.Hour)
		r.errorCode = ""
		r.mu.Unlock()
		return
	}
	if response.StatusCode != http.StatusOK {
		delay := 5 * time.Minute
		if response.StatusCode == 429 {
			delay = boundedRetryAfter(response.Header.Get("Retry-After"), time.Now())
		}
		if response.StatusCode == 401 || response.StatusCode == 403 {
			delay = 24 * time.Hour
		}
		r.failed("REGISTRY_UNAVAILABLE", delay)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, torRegistryMaxBody+1))
	if err != nil || len(body) > torRegistryMaxBody {
		r.failed("REGISTRY_RESPONSE", 5*time.Minute)
		return
	}
	roles, published, err := decodeTorRegistry(body, time.Now().UTC())
	if err != nil {
		r.failed("REGISTRY_RESPONSE", 5*time.Minute)
		return
	}
	r.mu.Lock()
	r.roles, r.published, r.checked = roles, published, time.Now().UTC()
	r.modified = response.Header.Get("Last-Modified")
	r.nextRefresh = time.Now().Add(time.Hour)
	r.refreshing = false
	r.errorCode = ""
	r.mu.Unlock()
}

func (r *TorRegistry) failed(code string, delay time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshing = false
	r.errorCode = code
	r.nextRefresh = time.Now().Add(delay)
}

func decodeTorRegistry(body []byte, now time.Time) (map[netip.Addr]uint8, time.Time, error) {
	var data struct {
		Published string `json:"relays_published"`
		Relays    []struct {
			ORAddresses   []string `json:"or_addresses"`
			ExitAddresses []string `json:"exit_addresses"`
			Flags         []string `json:"flags"`
			LastSeen      string   `json:"last_seen"`
		} `json:"relays"`
	}
	invalid := errors.New("invalid public Tor registry data")
	if len(body) > torRegistryMaxBody || json.Unmarshal(body, &data) != nil || data.Relays == nil || len(data.Relays) > 30000 {
		return nil, time.Time{}, invalid
	}
	published, err := time.Parse("2006-01-02 15:04:05", data.Published)
	if err != nil || published.After(now.Add(5*time.Minute)) || now.Sub(published) > 6*time.Hour {
		return nil, time.Time{}, invalid
	}
	result := make(map[netip.Addr]uint8)
	for _, relay := range data.Relays {
		lastSeen, err := time.Parse("2006-01-02 15:04:05", relay.LastSeen)
		if err != nil || now.Sub(lastSeen) > 6*time.Hour || lastSeen.After(now.Add(5*time.Minute)) {
			continue
		}
		guard, running := false, false
		for _, flag := range relay.Flags {
			if flag == "Guard" {
				guard = true
			}
			if flag == "Running" {
				running = true
			}
		}
		if !running {
			continue
		}
		for _, raw := range relay.ORAddresses {
			// OR addresses may contain a port list, so split host from port text.
			host, _, err := net.SplitHostPort(raw)
			if err != nil {
				continue
			}
			ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
			if err != nil {
				continue
			}
			ip, err = quality.PublicIP(ip)
			if err != nil {
				continue
			}
			bits := torRelay
			if guard {
				bits |= torGuard
			}
			result[ip] |= bits
		}
		for _, raw := range relay.ExitAddresses {
			ip, err := netip.ParseAddr(raw)
			if err != nil {
				continue
			}
			ip, err = quality.PublicIP(ip)
			if err == nil {
				result[ip] |= torExit
			}
		}
	}
	return result, published, nil
}
