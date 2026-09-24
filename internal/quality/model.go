// Package quality defines provider evidence independently of node health.
package quality

import (
	"errors"
	"net/netip"
	"strings"
	"time"
)

const (
	ProviderID       = "proxycheck"
	ProfileID        = "proxycheck-v3-risk-v1"
	AbuseProviderID  = "abuseipdb"
	AbuseProfileID   = "abuseipdb-v2-30d-v1"
	MaxRecords       = 65536
	MaxEvidenceBytes = 4096
)

var (
	ErrDisabled      = errors.New("quality inspection is disabled")
	ErrUnsupportedIP = errors.New("quality inspection requires a public IP address")
	ErrQueueFull     = errors.New("quality inspection queue is full")
	ErrStorageFull   = errors.New("quality evidence storage limit reached")
)

// Pointers distinguish an observed negative signal from missing provider data.
type Signals struct {
	Proxy       *bool `json:"proxy"`
	VPN         *bool `json:"vpn"`
	Tor         *bool `json:"tor"`
	Hosting     *bool `json:"hosting"`
	Compromised *bool `json:"compromised"`
	Scraper     *bool `json:"scraper"`
	Anonymous   *bool `json:"anonymous"`
}

type Evidence struct {
	ID               string         `json:"id"`
	IP               string         `json:"ip"`
	Provider         string         `json:"provider"`
	Profile          string         `json:"profile"`
	IPType           string         `json:"ip_type"`
	SourceType       string         `json:"source_type"`
	ASN              string         `json:"asn"`
	Organization     string         `json:"organization"`
	CountryCode      string         `json:"country_code"`
	NetworkProvider  string         `json:"network_provider,omitempty"`
	Operator         string         `json:"operator,omitempty"`
	OperatorServices []string       `json:"operator_services,omitempty"`
	AttackHistory    map[string]int `json:"attack_history,omitempty"`
	Native           *bool          `json:"native,omitempty"`
	TorRoles         []string       `json:"tor_roles,omitempty"`
	// RiskScore is the provider's original value: lower means lower risk.
	// We deliberately do not invent a calibrated "purity percentage".
	RiskScore         *int       `json:"risk_score"`
	Grade             string     `json:"grade"`
	SourceConfidence  *int       `json:"source_confidence"`
	Signals           Signals    `json:"signals"`
	ObservedAt        time.Time  `json:"observed_at"`
	ValidUntil        time.Time  `json:"valid_until"`
	SourceUpdatedAt   *time.Time `json:"source_updated_at,omitempty"`
	AbuseConfidence   *int       `json:"abuse_confidence,omitempty"`
	TotalReports      *int       `json:"total_reports,omitempty"`
	DistinctReporters *int       `json:"distinct_reporters,omitempty"`
	LastReportedAt    *time.Time `json:"last_reported_at,omitempty"`
	ReportWindowDays  int        `json:"report_window_days,omitempty"`

	// WP09 additions. Every field is optional so the existing decoders and the
	// evidence already stored in intel.db keep decoding unchanged.
	ASNNumber     int      `json:"asn_number,omitempty"`
	City          string   `json:"city,omitempty"`
	Region        string   `json:"region,omitempty"`
	RegisteredCC  string   `json:"registered_country,omitempty"`
	UsageType     string   `json:"usage_type,omitempty"`
	IsMobile      *bool    `json:"is_mobile,omitempty"`
	IsResidential *bool    `json:"is_residential,omitempty"`
	FraudScore    *int     `json:"fraud_score,omitempty"`
	DNSBLListed   []string `json:"dnsbl_listed,omitempty"`
	DNSBLChecked  []string `json:"dnsbl_checked,omitempty"`
}

type Task struct {
	IP            string    `json:"ip"`
	Provider      string    `json:"provider"`
	Profile       string    `json:"profile"`
	State         string    `json:"state"`
	Generation    int64     `json:"generation"`
	Attempt       int       `json:"attempt"`
	RequestedAt   time.Time `json:"requested_at"`
	NextRunAt     time.Time `json:"next_run_at"`
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LeaseUntil    time.Time `json:"-"`
	Owner         string    `json:"-"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
}

type Record struct {
	Task     Task
	Evidence *Evidence
}

type Completion struct {
	Evidence     *Evidence
	ErrorCode    string
	ErrorMessage string
	RetryAt      time.Time
	BlockedUntil time.Time
	Pause        bool
	Canceled     bool
}

type Summary struct {
	Assessment *Assessment     `json:"assessment,omitempty"`
	IP         string          `json:"ip"`
	State      string          `json:"state"`
	Evidence   *Evidence       `json:"evidence"`
	Task       *Task           `json:"task,omitempty"`
	Sources    []SourceSummary `json:"sources"`
}

type SourceSummary struct {
	Provider   string    `json:"provider"`
	Configured bool      `json:"configured"`
	State      string    `json:"state"`
	Evidence   *Evidence `json:"evidence"`
	Task       *Task     `json:"task,omitempty"`
}

func (r Record) Summary(now time.Time) Summary {
	s := Summary{IP: r.Task.IP, State: "unobserved", Evidence: r.Evidence}
	task := r.Task
	s.Task = &task
	if r.Evidence != nil {
		s.State = "valid"
		if !now.Before(r.Evidence.ValidUntil) {
			s.State = "stale"
		} else if r.Evidence.IPType == "conflicting" {
			s.State = "conflicting"
		}
	} else if task.State == "queued" || task.State == "running" {
		s.State = "pending"
	} else if task.ErrorCode == "UNSUPPORTED_IP" {
		s.State = "unsupported"
	}
	return s
}

type Budget struct {
	Provider      string
	Day           string
	Used          int
	NextRequestAt time.Time
	BlockedUntil  time.Time
	ErrorCode     string
	CredentialID  string
	Paused        bool
}

func RiskGrade(risk *int, signals Signals) string {
	if signals.Compromised != nil && *signals.Compromised {
		return "severe"
	}
	if risk == nil {
		return "unknown"
	}
	switch {
	case *risk <= 25:
		return "low"
	case *risk <= 50:
		return "moderate"
	case *risk <= 75:
		return "high"
	default:
		return "severe"
	}
}

// EffectiveRiskGrade does not average scores with different meanings. A
// current, high-confidence abuse report requires review even if the primary
// source is unavailable or reports a low risk score.
func EffectiveRiskGrade(summary Summary) string {
	for _, source := range summary.Sources {
		e := source.Evidence
		if source.State == "valid" && e != nil && e.AbuseConfidence != nil && *e.AbuseConfidence >= 75 &&
			e.TotalReports != nil && *e.TotalReports > 0 {
			return "review"
		}
	}
	if summary.State != "valid" || summary.Evidence == nil {
		return "unknown"
	}
	return summary.Evidence.Grade
}

// PurityScore is an explicitly labelled display inversion, not a calibrated
// probability or a second provider score. Missing or invalid evidence stays unknown.
func PurityScore(risk *int) *int {
	if risk == nil || *risk < 0 || *risk > 100 {
		return nil
	}
	score := 100 - *risk
	return &score
}

// PurityBand defines Prism's product bands, independently of provider risk bands.
func PurityBand(score *int) string {
	if score == nil || *score < 0 || *score > 100 {
		return "unknown"
	}
	switch {
	case *score >= 95:
		return "excellent"
	case *score >= 90:
		return "clean"
	case *score >= 80:
		return "fair"
	case *score >= 60:
		return "mixed"
	default:
		return "poor"
	}
}

func EffectivePurityBand(summary Summary) string {
	if summary.Assessment == nil {
		return "unknown"
	}
	return summary.Assessment.PurityBand
}

// NetworkType preserves allocation type separately from anonymity signals.
// Wireless includes satellite links and must not be relabelled as mobile.
func NetworkType(raw string, signals Signals) string {
	kind := "unknown"
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "residential":
		kind = "residential"
	case "hosting", "datacenter", "data center":
		kind = "datacenter"
	case "business":
		kind = "business"
	case "wireless":
		kind = "wireless"
	case "mobile", "cellular":
		kind = "mobile"
	}
	if signals.Hosting != nil && *signals.Hosting {
		if kind != "unknown" && kind != "datacenter" {
			return "conflicting"
		}
		return "datacenter"
	}
	return kind
}

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"),
}

func PublicIP(ip netip.Addr) (netip.Addr, error) {
	if ip.Zone() != "" {
		return netip.Addr{}, ErrUnsupportedIP
	}
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return netip.Addr{}, ErrUnsupportedIP
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return netip.Addr{}, ErrUnsupportedIP
		}
	}
	return ip, nil
}
