// Package intel owns the persistent intel.db store, the in-memory projection it
// feeds, and the batch job system (WP08).
package intel

import (
	"context"
	"math"
	"net/netip"
	"strings"
	"sync"

	"prism/internal/intel/assess"
	"prism/internal/intel/store"
)

// Band codes used by AssessmentLite (WP08 §5).
const (
	BandUnknown uint8 = iota
	BandExcellent
	BandClean
	BandFair
	BandMixed
	BandPoor
)

// Confidence codes used by AssessmentLite.
const (
	ConfidenceNone uint8 = iota
	ConfidenceLow
	ConfidenceMedium
	ConfidenceHigh
)

// Verdict codes used by AssessmentLite (WP10 §1.6).
const (
	VerdictPending uint8 = iota
	VerdictFavorable
	VerdictCaution
	VerdictIncomplete
	VerdictReview
	VerdictConflicting
	VerdictHighRisk
)

// IP type codes used by AssessmentLite (WP10 §1.4).
const (
	IPTypeUnknown uint8 = iota
	IPTypeResidential
	IPTypeMobile
	IPTypeBusiness
	IPTypeWireless
	IPTypeDatacenter
	IPTypeNonResidential
	IPTypeConflicting
)

// Assessment state codes used by AssessmentLite (WP10 §1.1). They mirror the
// stored ip_assessment.state values, so the node list can report pending vs
// unsupported without reading the database (WP10 §4).
const (
	StatePending uint8 = iota
	StateValid
	StateUnsupported
)

// Assessment flag bits (WP10 §1.1). The routing admission path only needs a
// bitmask, so the projection keeps 16 bits per IP instead of the full reasons.
// The bits are aliases of the scorer's flags so the projection and prism-purity-v2
// can never drift apart.
const (
	FlagCompromised   uint16 = uint16(assess.FlagCompromised)
	FlagTor           uint16 = uint16(assess.FlagTor)
	FlagVPN           uint16 = uint16(assess.FlagVPN)
	FlagProxy         uint16 = uint16(assess.FlagProxy)
	FlagScraper       uint16 = uint16(assess.FlagScraper)
	FlagAbuse         uint16 = uint16(assess.FlagAbuse)
	FlagAnonymous     uint16 = uint16(assess.FlagAnonymous)
	FlagHosting       uint16 = uint16(assess.FlagHosting)
	FlagDNSBL         uint16 = uint16(assess.FlagDNSBL)
	FlagAttackHistory uint16 = uint16(assess.FlagAttackHistory)
)

// flagNames maps each bit to its stable API name.
var flagNames = []struct {
	Bit  uint16
	Name string
}{
	{FlagCompromised, "compromised"},
	{FlagTor, "tor"},
	{FlagVPN, "vpn"},
	{FlagProxy, "proxy"},
	{FlagScraper, "scraper"},
	{FlagAbuse, "abuse"},
	{FlagAnonymous, "anonymous"},
	{FlagHosting, "hosting"},
	{FlagDNSBL, "dnsbl"},
	{FlagAttackHistory, "attack_history"},
}

// FlagNames renders a flag bitmask as the API string list.
func FlagNames(flags uint16) []string {
	out := make([]string, 0, len(flagNames))
	for _, entry := range flagNames {
		if flags&entry.Bit != 0 {
			out = append(out, entry.Name)
		}
	}
	return out
}

// FlagFromName maps an API flag name to its bit, or 0 when unknown.
func FlagFromName(name string) uint16 {
	for _, entry := range flagNames {
		if entry.Name == name {
			return entry.Bit
		}
	}
	return 0
}

var bandCodes = map[string]uint8{
	"unknown": BandUnknown, "excellent": BandExcellent, "clean": BandClean,
	"fair": BandFair, "mixed": BandMixed, "poor": BandPoor,
}

var bandNames = []string{"unknown", "excellent", "clean", "fair", "mixed", "poor"}

var confidenceCodes = map[string]uint8{
	"none": ConfidenceNone, "low": ConfidenceLow,
	"medium": ConfidenceMedium, "high": ConfidenceHigh,
}

var confidenceNames = []string{"none", "low", "medium", "high"}

var verdictCodes = map[string]uint8{
	"pending": VerdictPending, "favorable": VerdictFavorable, "caution": VerdictCaution,
	"incomplete": VerdictIncomplete, "review": VerdictReview,
	"conflicting": VerdictConflicting, "high_risk": VerdictHighRisk,
}

var verdictNames = []string{"pending", "favorable", "caution", "incomplete", "review", "conflicting", "high_risk"}

var ipTypeCodes = map[string]uint8{
	"unknown": IPTypeUnknown, "residential": IPTypeResidential, "mobile": IPTypeMobile,
	"business": IPTypeBusiness, "wireless": IPTypeWireless, "datacenter": IPTypeDatacenter,
	"non_residential": IPTypeNonResidential, "conflicting": IPTypeConflicting,
}

var ipTypeNames = []string{"unknown", "residential", "mobile", "business", "wireless", "datacenter", "non_residential", "conflicting"}

var stateCodes = map[string]uint8{
	"pending": StatePending, "valid": StateValid, "unsupported": StateUnsupported,
}

var stateNames = []string{"pending", "valid", "unsupported"}

// BandCode encodes a purity band name; unknown values become BandUnknown.
func BandCode(name string) uint8 { return codeOf(bandCodes, name) }

// BandName decodes a purity band code.
func BandName(code uint8) string { return nameOf(bandNames, code) }

// ConfidenceCode encodes a confidence name.
func ConfidenceCode(name string) uint8 { return codeOf(confidenceCodes, name) }

// ConfidenceName decodes a confidence code.
func ConfidenceName(code uint8) string { return nameOf(confidenceNames, code) }

// VerdictCode encodes a verdict name.
func VerdictCode(name string) uint8 { return codeOf(verdictCodes, name) }

// VerdictName decodes a verdict code.
func VerdictName(code uint8) string { return nameOf(verdictNames, code) }

// VerdictValues returns every §1.6 verdict name, in code order. The API filter
// vocabulary is built from it, so validation can never drift from the
// projection.
func VerdictValues() []string { return append([]string(nil), verdictNames...) }

// IPTypeCode encodes an IP type name.
func IPTypeCode(name string) uint8 { return codeOf(ipTypeCodes, name) }

// IPTypeName decodes an IP type code.
func IPTypeName(code uint8) string { return nameOf(ipTypeNames, code) }

// StateCode encodes an assessment state name; unknown values become StatePending.
func StateCode(name string) uint8 { return codeOf(stateCodes, name) }

// StateName decodes an assessment state code.
func StateName(code uint8) string { return nameOf(stateNames, code) }

func codeOf(table map[string]uint8, name string) uint8 {
	if code, ok := table[strings.ToLower(strings.TrimSpace(name))]; ok {
		return code
	}
	return 0
}

func nameOf(names []string, code uint8) string {
	if int(code) < len(names) {
		return names[code]
	}
	return names[0]
}

// AssessmentLite is the in-memory projection of one ip_assessment row. Score is
// -1 when the purity score is NULL and Native is -1 when it is unknown (WP08 §5).
//
// Score, Band, Verdict, IPType, Confidence, Native, Flags, ValidUntil and
// ComputedAt are the compact subset the routing admission path reads. State and
// the row-level display facts (ASN, AS org, country, city) travel with them so
// WP10 §4 can render a node's assessment without a database read on the request
// path.
type AssessmentLite struct {
	Score      int8
	Band       uint8
	Verdict    uint8
	IPType     uint8
	Confidence uint8
	State      uint8
	Native     int8
	Flags      uint16
	ASN        int32
	ASOrg      string
	Country    string
	City       string
	ValidUntil int64
	ComputedAt int64
}

// ScoreValue returns the purity score pointer (nil when unknown).
func (a AssessmentLite) ScoreValue() *int {
	if a.Score < 0 {
		return nil
	}
	v := int(a.Score)
	return &v
}

// NativeValue returns the native flag pointer (nil when unknown).
func (a AssessmentLite) NativeValue() *bool {
	switch a.Native {
	case 0:
		v := false
		return &v
	case 1:
		v := true
		return &v
	default:
		return nil
	}
}

// ASNValue returns the autonomous system number, or 0 when it is unknown.
func (a AssessmentLite) ASNValue() int {
	if a.ASN <= 0 {
		return 0
	}
	return int(a.ASN)
}

// StateName returns the projection state as its API name.
func (a AssessmentLite) StateName() string { return StateName(a.State) }

// Valid reports whether the assessment is still valid at nowNs.
func (a AssessmentLite) Valid(nowNs int64) bool {
	return a.ValidUntil > nowNs
}

// AssessmentLiteFromRow converts a stored assessment into its projection.
func AssessmentLiteFromRow(row store.Assessment) AssessmentLite {
	lite := AssessmentLite{
		Score:      -1,
		Band:       BandCode(row.PurityBand),
		Verdict:    VerdictCode(row.Verdict),
		IPType:     IPTypeCode(row.IPType),
		Confidence: ConfidenceCode(row.Confidence),
		State:      StateCode(row.State),
		Native:     -1,
		Flags:      uint16(row.Flags),
		ASOrg:      row.ASOrg,
		Country:    row.Country,
		City:       row.City,
		ValidUntil: row.ValidUntilNs,
		ComputedAt: row.ComputedAtNs,
	}
	if score := row.ScoreOrNil(); score != nil {
		if *score > 127 {
			lite.Score = 127
		} else {
			lite.Score = int8(*score)
		}
	}
	if native := row.NativeOrNil(); native != nil {
		lite.Native = int8(*native)
	}
	if asn := row.ASNOrNil(); asn != nil && *asn > 0 && *asn <= math.MaxInt32 {
		lite.ASN = int32(*asn)
	}
	return lite
}

// OutcomeLite is the projection of one node_checks row.
type OutcomeLite struct {
	Outcome    uint8
	ValidUntil int64
	ObservedAt int64
}

// Check outcome codes.
const (
	OutcomeUnknown uint8 = iota
	OutcomeAvailable
	OutcomeBlocked
	OutcomeRegionLimited
	OutcomeCaptcha
	OutcomeError
)

var outcomeCodes = map[string]uint8{
	"unknown": OutcomeUnknown, "available": OutcomeAvailable, "blocked": OutcomeBlocked,
	"region_limited": OutcomeRegionLimited, "captcha": OutcomeCaptcha, "error": OutcomeError,
}

var outcomeNames = []string{"unknown", "available", "blocked", "region_limited", "captcha", "error"}

// OutcomeCode encodes a check outcome name.
func OutcomeCode(name string) uint8 { return codeOf(outcomeCodes, name) }

// OutcomeName decodes a check outcome code.
func OutcomeName(code uint8) string { return nameOf(outcomeNames, code) }

// OutcomeValues returns every check outcome name, in code order. The API
// `check=<id>:<outcome>` filter is validated against it.
func OutcomeValues() []string { return append([]string(nil), outcomeNames...) }

// Valid reports whether the check result is still valid at nowNs.
func (o OutcomeLite) Valid(nowNs int64) bool { return o.ValidUntil > nowNs }

// SnapshotReader is the read-only view the routing admission path and the node
// list use (WP10 §2 and §4). It never touches the database.
type SnapshotReader interface {
	Assessment(ip netip.Addr) (AssessmentLite, bool)
	CheckOutcome(nodeHash, checkID string) (OutcomeLite, bool)
}

// SnapshotSource loads the full projection from the authoritative store.
type SnapshotSource interface {
	ListAllAssessments(ctx context.Context) ([]store.Assessment, error)
	ListAllNodeChecks(ctx context.Context) ([]store.NodeCheck, error)
}

// Snapshot is the in-memory projection of ip_assessment and node_checks. It is
// only a cache: every entry can be rebuilt from intel.db on restart (§5).
type Snapshot struct {
	mu          sync.RWMutex
	assessments map[netip.Addr]AssessmentLite
	checks      map[string]map[string]OutcomeLite
}

// NewSnapshot returns an empty projection.
func NewSnapshot() *Snapshot {
	return &Snapshot{
		assessments: make(map[netip.Addr]AssessmentLite),
		checks:      make(map[string]map[string]OutcomeLite),
	}
}

// LoadFrom rebuilds the projection from the store. It replaces the current
// contents atomically so readers never observe a half-loaded cache.
func (s *Snapshot) LoadFrom(ctx context.Context, src SnapshotSource) error {
	if s == nil || src == nil {
		return nil
	}
	assessments, err := src.ListAllAssessments(ctx)
	if err != nil {
		return err
	}
	checks, err := src.ListAllNodeChecks(ctx)
	if err != nil {
		return err
	}

	nextAssessments := make(map[netip.Addr]AssessmentLite, len(assessments))
	for _, row := range assessments {
		ip, err := netip.ParseAddr(row.IP)
		if err != nil {
			continue
		}
		nextAssessments[ip.Unmap()] = AssessmentLiteFromRow(row)
	}
	nextChecks := make(map[string]map[string]OutcomeLite)
	for _, row := range checks {
		entry, ok := nextChecks[row.NodeHash]
		if !ok {
			entry = make(map[string]OutcomeLite)
			nextChecks[row.NodeHash] = entry
		}
		entry[row.CheckID] = OutcomeLite{
			Outcome:    OutcomeCode(row.Outcome),
			ValidUntil: row.ValidUntilNs,
			ObservedAt: row.ObservedAtNs,
		}
	}

	s.mu.Lock()
	s.assessments = nextAssessments
	s.checks = nextChecks
	s.mu.Unlock()
	return nil
}

// SetAssessment updates one projected assessment after a successful write.
func (s *Snapshot) SetAssessment(ip netip.Addr, lite AssessmentLite) {
	if s == nil || !ip.IsValid() {
		return
	}
	s.mu.Lock()
	s.assessments[ip.Unmap()] = lite
	s.mu.Unlock()
}

// SetAssessmentFromRow projects and stores one assessment row.
func (s *Snapshot) SetAssessmentFromRow(row store.Assessment) {
	ip, err := netip.ParseAddr(row.IP)
	if err != nil {
		return
	}
	s.SetAssessment(ip, AssessmentLiteFromRow(row))
}

// SetCheck updates one projected check result.
func (s *Snapshot) SetCheck(nodeHash, checkID string, lite OutcomeLite) {
	if s == nil || nodeHash == "" || checkID == "" {
		return
	}
	s.mu.Lock()
	entry, ok := s.checks[nodeHash]
	if !ok {
		entry = make(map[string]OutcomeLite)
		s.checks[nodeHash] = entry
	}
	entry[checkID] = lite
	s.mu.Unlock()
}

// DeleteNodeChecks drops every projected check of one node.
func (s *Snapshot) DeleteNodeChecks(nodeHash string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.checks, nodeHash)
	s.mu.Unlock()
}

// Assessment returns the projected assessment of an IP.
func (s *Snapshot) Assessment(ip netip.Addr) (AssessmentLite, bool) {
	if s == nil || !ip.IsValid() {
		return AssessmentLite{}, false
	}
	s.mu.RLock()
	lite, ok := s.assessments[ip.Unmap()]
	s.mu.RUnlock()
	return lite, ok
}

// CheckOutcome returns the projected check result of one node.
func (s *Snapshot) CheckOutcome(nodeHash, checkID string) (OutcomeLite, bool) {
	if s == nil {
		return OutcomeLite{}, false
	}
	s.mu.RLock()
	entry, ok := s.checks[nodeHash]
	if !ok {
		s.mu.RUnlock()
		return OutcomeLite{}, false
	}
	lite, ok := entry[checkID]
	s.mu.RUnlock()
	return lite, ok
}

// NodeCheckOutcomes returns a copy of every check result of one node.
func (s *Snapshot) NodeCheckOutcomes(nodeHash string) map[string]OutcomeLite {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry := s.checks[nodeHash]
	if len(entry) == 0 {
		return nil
	}
	out := make(map[string]OutcomeLite, len(entry))
	for id, lite := range entry {
		out[id] = lite
	}
	return out
}

// Len reports the number of projected assessments and nodes with checks.
func (s *Snapshot) Len() (assessments, nodesWithChecks int) {
	if s == nil {
		return 0, 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.assessments), len(s.checks)
}

// Clear empties the projection.
func (s *Snapshot) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.assessments = make(map[netip.Addr]AssessmentLite)
	s.checks = make(map[string]map[string]OutcomeLite)
	s.mu.Unlock()
}

var _ SnapshotReader = (*Snapshot)(nil)
