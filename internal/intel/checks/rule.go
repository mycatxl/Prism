// Package checks implements the rule-driven unlock detection engine of WP09 §5:
// YAML rules (built in and user supplied), the matcher set, and the executor
// that runs a rule's steps through the node under test.
package checks

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Outcomes of one unlock check (WP08 §2 node_checks.outcome).
const (
	OutcomeAvailable     = "available"
	OutcomeBlocked       = "blocked"
	OutcomeRegionLimited = "region_limited"
	OutcomeCaptcha       = "captcha"
	OutcomeError         = "error"
	OutcomeUnknown       = "unknown"
)

// Limits of one rule (WP09 §5.3).
const (
	// DefaultTimeout bounds one whole check.
	DefaultTimeout = 10 * time.Second
	// MaxBodyBytes caps the response body a step reads.
	MaxBodyBytes = 256 * 1024
	// MaxDetailBytes bounds the stored per-step detail.
	MaxDetailBytes = 4 * 1024
	// DefaultTTL is the validity of one result.
	DefaultTTL = 24 * time.Hour
	// MaxSteps bounds one rule.
	MaxSteps = 8
	// MaxOutcomes bounds one rule's outcome list.
	MaxOutcomes = 32
)

// Duration is a YAML duration such as "24h" or "10s".
type Duration time.Duration

// Duration returns the parsed duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// UnmarshalYAML decodes a Go duration string.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var raw string
	if err := node.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML renders the duration back as a Go duration string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Request is one HTTP step.
type Request struct {
	Method          string            `yaml:"method"`
	URL             string            `yaml:"url"`
	FollowRedirects bool              `yaml:"follow_redirects"`
	Headers         map[string]string `yaml:"headers"`
	MaxBodyBytes    int               `yaml:"max_body_bytes"`
	Body            string            `yaml:"body"`
}

// TCP is one TCP-connect step.
type TCP struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// Step is one step of a rule.
type Step struct {
	ID      string   `yaml:"id"`
	Request *Request `yaml:"request"`
	TCP     *TCP     `yaml:"tcp"`
}

// When is one outcome condition. Every present field must match (AND).
type When struct {
	Step           string            `yaml:"step"`
	StatusIn       []int             `yaml:"status_in"`
	StatusNotIn    []int             `yaml:"status_not_in"`
	HeaderContains map[string]string `yaml:"header_contains"`
	HeaderRegex    map[string]string `yaml:"header_regex"`
	BodyContains   string            `yaml:"body_contains"`
	BodyNotContain string            `yaml:"body_not_contains"`
	BodyRegex      string            `yaml:"body_regex"`
	RedirectHost   string            `yaml:"redirect_host"`
	Connected      *bool             `yaml:"connected"`
	Error          string            `yaml:"error"`
}

// OutcomeCase is one ordered outcome rule.
type OutcomeCase struct {
	When    When   `yaml:"when"`
	Outcome string `yaml:"outcome"`

	headerRegex []*regexp.Regexp
	bodyRegex   *regexp.Regexp
}

// RegionSpec extracts the region a check inferred from one step. The region is
// normally read from the body; HeaderRegex covers providers that only expose it
// in a response header, such as the Location of a redirect: Netflix answers 301
// with an empty body and encodes the region in the "/jp-en/" path prefix.
type RegionSpec struct {
	Step        string            `yaml:"step"`
	BodyRegex   string            `yaml:"body_regex"`
	HeaderRegex map[string]string `yaml:"header_regex"`

	compiled    *regexp.Regexp
	headerRegex []headerRegionPattern
}

// headerRegionPattern binds one region header matcher to the header it reads.
type headerRegionPattern struct {
	name string
	re   *regexp.Regexp
}

// Rule is one unlock check rule.
type Rule struct {
	ID         string        `yaml:"id"`
	Version    int           `yaml:"version"`
	Name       string        `yaml:"name"`
	Category   string        `yaml:"category"`
	Enabled    bool          `yaml:"enabled"`
	TTL        Duration      `yaml:"ttl"`
	Timeout    Duration      `yaml:"timeout"`
	Steps      []Step        `yaml:"steps"`
	Outcomes   []OutcomeCase `yaml:"outcomes"`
	Default    string        `yaml:"default"`
	Region     *RegionSpec   `yaml:"region"`
	Calibrated string        `yaml:"calibrated"`

	// Source is "builtin" or "user"; Path is where the rule was loaded from.
	Source string `yaml:"-"`
	Path   string `yaml:"-"`
}

// RuleIDPattern is the accepted rule id shape.
var RuleIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// ValidOutcomes is the accepted outcome set.
var ValidOutcomes = map[string]bool{
	OutcomeAvailable: true, OutcomeBlocked: true, OutcomeRegionLimited: true,
	OutcomeCaptcha: true, OutcomeError: true, OutcomeUnknown: true,
}

var validCategories = map[string]bool{
	"ai": true, "video": true, "music": true, "social": true,
	"search": true, "mail": true, "other": true,
}

// TimeoutOr returns the rule timeout, defaulting to DefaultTimeout.
func (r *Rule) TimeoutOr() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout.Duration()
	}
	return DefaultTimeout
}

// TTLOr returns the rule TTL, defaulting to DefaultTTL.
func (r *Rule) TTLOr() time.Duration {
	if r.TTL > 0 {
		return r.TTL.Duration()
	}
	return DefaultTTL
}

// ParseRule decodes and validates one YAML rule.
func ParseRule(raw []byte, source, path string) (*Rule, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var rule Rule
	if err := decoder.Decode(&rule); err != nil {
		return nil, fmt.Errorf("decode rule %s: %w", path, err)
	}
	rule.Source = source
	rule.Path = path
	if err := rule.Validate(); err != nil {
		return nil, fmt.Errorf("rule %s: %w", path, err)
	}
	return &rule, nil
}

// Validate checks the rule shape and compiles its regular expressions.
func (r *Rule) Validate() error {
	if !RuleIDPattern.MatchString(r.ID) {
		return fmt.Errorf("id %q must match %s", r.ID, RuleIDPattern)
	}
	if r.Version <= 0 {
		return errors.New("version must be a positive integer")
	}
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("name is required")
	}
	if !validCategories[r.Category] {
		return fmt.Errorf("category %q is not one of ai|video|music|social|search|mail|other", r.Category)
	}
	if len(r.Steps) == 0 || len(r.Steps) > MaxSteps {
		return fmt.Errorf("steps must contain 1..%d entries", MaxSteps)
	}
	stepIDs := make(map[string]bool, len(r.Steps))
	for _, step := range r.Steps {
		if !RuleIDPattern.MatchString(step.ID) {
			return fmt.Errorf("step id %q must match %s", step.ID, RuleIDPattern)
		}
		if stepIDs[step.ID] {
			return fmt.Errorf("duplicate step id %q", step.ID)
		}
		stepIDs[step.ID] = true
		switch {
		case step.Request != nil && step.TCP != nil:
			return fmt.Errorf("step %q must be either a request or a tcp step", step.ID)
		case step.Request == nil && step.TCP == nil:
			return fmt.Errorf("step %q needs a request or a tcp block", step.ID)
		case step.Request != nil:
			if !strings.HasPrefix(step.Request.URL, "https://") && !strings.HasPrefix(step.Request.URL, "http://") {
				return fmt.Errorf("step %q url must be absolute", step.ID)
			}
			if step.Request.MaxBodyBytes < 0 || step.Request.MaxBodyBytes > MaxBodyBytes {
				return fmt.Errorf("step %q max_body_bytes must be between 0 and %d", step.ID, MaxBodyBytes)
			}
		case step.TCP != nil:
			if strings.TrimSpace(step.TCP.Host) == "" || step.TCP.Port <= 0 || step.TCP.Port > 65535 {
				return fmt.Errorf("step %q tcp needs a host and a port", step.ID)
			}
		}
	}
	if len(r.Outcomes) > MaxOutcomes {
		return fmt.Errorf("outcomes must contain at most %d entries", MaxOutcomes)
	}
	for i := range r.Outcomes {
		outcome := &r.Outcomes[i]
		if !ValidOutcomes[outcome.Outcome] {
			return fmt.Errorf("outcome %q is not a valid outcome", outcome.Outcome)
		}
		if outcome.When.Step != "" && !stepIDs[outcome.When.Step] {
			return fmt.Errorf("outcome references unknown step %q", outcome.When.Step)
		}
		if outcome.When.Error != "" {
			switch outcome.When.Error {
			case "timeout", "refused", "tls", "any":
			default:
				return fmt.Errorf("error condition %q must be timeout|refused|tls|any", outcome.When.Error)
			}
		}
		outcome.headerRegex = make([]*regexp.Regexp, 0, len(outcome.When.HeaderRegex))
		for name, pattern := range outcome.When.HeaderRegex {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("header_regex %s: %w", name, err)
			}
			outcome.headerRegex = append(outcome.headerRegex, compiled)
		}
		if outcome.When.BodyRegex != "" {
			compiled, err := regexp.Compile(outcome.When.BodyRegex)
			if err != nil {
				return fmt.Errorf("body_regex: %w", err)
			}
			outcome.bodyRegex = compiled
		}
	}
	if r.Default == "" {
		r.Default = OutcomeUnknown
	}
	if !ValidOutcomes[r.Default] {
		return fmt.Errorf("default %q is not a valid outcome", r.Default)
	}
	if r.Region != nil {
		if r.Region.Step != "" && !stepIDs[r.Region.Step] {
			return fmt.Errorf("region references unknown step %q", r.Region.Step)
		}
		if r.Region.BodyRegex == "" && len(r.Region.HeaderRegex) == 0 {
			return errors.New("region needs a body_regex or a header_regex")
		}
		if r.Region.BodyRegex != "" {
			compiled, err := compileRegionPattern("region body_regex", r.Region.BodyRegex)
			if err != nil {
				return err
			}
			r.Region.compiled = compiled
		}
		// Header patterns are compiled in a stable order so extraction is
		// deterministic when more than one header is configured.
		names := make([]string, 0, len(r.Region.HeaderRegex))
		for name := range r.Region.HeaderRegex {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if strings.TrimSpace(name) == "" {
				return errors.New("region header_regex needs a header name")
			}
			compiled, err := compileRegionPattern("region header_regex "+name, r.Region.HeaderRegex[name])
			if err != nil {
				return err
			}
			r.Region.headerRegex = append(r.Region.headerRegex, headerRegionPattern{name: name, re: compiled})
		}
	}
	return nil
}

// compileRegionPattern compiles one region matcher. The engine reads the region
// from the FIRST capture group only: extra groups are tolerated for backwards
// compatibility with rules written before this helper existed (they are simply
// ignored), but at least one group is required.
func compileRegionPattern(kind, pattern string) (*regexp.Regexp, error) {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", kind, err)
	}
	if compiled.NumSubexp() < 1 {
		return nil, fmt.Errorf("%s needs a capture group", kind)
	}
	return compiled, nil
}

// ValidateUserFileName accepts "<id>.yaml" and "<id>.yml" only, so a malformed
// file cannot shadow a different rule.
func ValidateUserFileName(name string) (string, bool) {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	if ext != ".yaml" && ext != ".yml" {
		return "", false
	}
	id := strings.TrimSuffix(base, ext)
	if !RuleIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// LoadDir reads every rule file of one directory. A broken file is reported and
// skipped: one bad user rule must not disable the whole engine.
func LoadDir(dir, source string) ([]*Rule, []error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var rules []*Rule
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := ValidateUserFileName(entry.Name())
		if !ok {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rule, err := ParseRule(raw, source, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if rule.ID != id {
			errs = append(errs, fmt.Errorf("rule %s: id %q must equal the file name", path, rule.ID))
			continue
		}
		rules = append(rules, rule)
	}
	return rules, errs
}

// LoadFS reads every rule file of an embedded filesystem (the built-ins).
func LoadFS(fsys fs.FS, dir, source string) ([]*Rule, []error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, []error{err}
	}
	var rules []*Rule
	var errs []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		id, ok := ValidateUserFileName(entry.Name())
		if !ok {
			continue
		}
		path := dir + "/" + entry.Name()
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rule, err := ParseRule(raw, source, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if rule.ID != id {
			errs = append(errs, fmt.Errorf("rule %s: id %q must equal the file name", path, rule.ID))
			continue
		}
		rules = append(rules, rule)
	}
	return rules, errs
}

// itoa renders an int without pulling in strconv at every call site.
func itoa(value int) string { return strconv.Itoa(value) }
