// Package jobs implements the persistent batch job system of WP08 §3: job
// creation and scope expansion, the per-node worker pool with a resumable step
// pipeline, the per-provider queue workers and the SSE progress hub.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"prism/internal/intel/store"
	"prism/internal/model"
)

// Kind is the job pipeline selector (WP08 §3.2).
type Kind string

const (
	KindEgress Kind = "egress"
	KindIntel  Kind = "intel"
	KindChecks Kind = "checks"
	KindFull   Kind = "full"
)

// ParseKind validates a job kind.
func ParseKind(raw string) (Kind, error) {
	switch Kind(strings.ToLower(strings.TrimSpace(raw))) {
	case KindEgress:
		return KindEgress, nil
	case KindIntel:
		return KindIntel, nil
	case KindChecks:
		return KindChecks, nil
	case KindFull:
		return KindFull, nil
	default:
		return "", fmt.Errorf("unknown job kind %q", raw)
	}
}

// Step is one numbered stage of the node pipeline.
type Step int

const (
	StepEgress        Step = 1
	StepOffline       Step = 2
	StepEnqueueOnline Step = 3
	StepViaNode       Step = 4
	StepChecks        Step = 5
	StepAssess        Step = 6
)

var kindPlans = map[Kind][]Step{
	KindEgress: {StepEgress},
	KindIntel:  {StepEgress, StepOffline, StepEnqueueOnline, StepViaNode, StepAssess},
	KindChecks: {StepEgress, StepChecks, StepAssess},
	KindFull:   {StepEgress, StepOffline, StepEnqueueOnline, StepViaNode, StepChecks, StepAssess},
}

// Plan returns the ordered steps of one kind.
func Plan(kind Kind) []Step {
	return kindPlans[kind]
}

// PendingSteps returns the steps after lastCompleted (0 = every step). The
// item's step_index stores the last completed step so a restart resumes at the
// following step (§3.2, §3.5).
func PendingSteps(kind Kind, lastCompleted int) []Step {
	plan := kindPlans[kind]
	out := make([]Step, 0, len(plan))
	for _, step := range plan {
		if int(step) > lastCompleted {
			out = append(out, step)
		}
	}
	return out
}

// Job priorities (§3.6).
const (
	PriorityManual       = 100
	PrioritySubscription = 50
	PriorityRefresh      = 10
)

// MaxNodesPerJob is the hard scope limit of §3.1.
const MaxNodesPerJob = 200_000

// Timing constants of §3.4 and §3.5.
const (
	itemTimeout          = 90 * time.Second
	itemLease            = 3 * time.Minute
	providerLease        = 2 * time.Minute
	maxItemAttempts      = 5
	maxProviderAttempts  = 5
	backoffBase          = 30 * time.Second
	backoffMax           = 6 * time.Hour
	defaultTick          = time.Second
	idlePollInterval     = 250 * time.Millisecond
	maxActiveJobsFetched = 64
)

// Default queue and pool bounds (R4).
const (
	// DefaultNodeWorkers is the default parallelism of the node pipeline.
	DefaultNodeWorkers = 16
	// DefaultMaxRunningJobs is the default number of jobs allowed to execute.
	DefaultMaxRunningJobs = 2
	// DefaultCheckConcurrencyPerCheck is the default global concurrency of one
	// unlock check rule.
	DefaultCheckConcurrencyPerCheck = 2
	// MaxNodeWorkers is the validated upper bound of intel_node_workers.
	MaxNodeWorkers = 128
	// SSEMaxSubscribers bounds the in-memory SSE fan-out. Over the limit the
	// subscription request fails with ErrTooManySubscribers (mapped to 429).
	SSEMaxSubscribers = 64
	// sseBufferSize bounds one subscriber queue; a slow client that fills it has
	// messages dropped and counted instead of blocking the worker pool.
	sseBufferSize = 8
)

// Errors surfaced to the API layer.
var (
	ErrDisabled            = errors.New("intel jobs are disabled")
	ErrTooManyNodes        = errors.New("job scope exceeds the node limit")
	ErrTooManySubscribers  = errors.New("too many intel SSE subscribers")
	ErrInvalidJob          = errors.New("invalid job request")
	ErrProviderNotRunnable = errors.New("provider worker is not runnable")
)

// Scope selects the nodes of a job. Every entry is a union (§3.1).
type Scope struct {
	All             bool              `json:"all"`
	SubscriptionIDs []string          `json:"subscription_ids"`
	PlatformIDs     []string          `json:"platform_ids"`
	NodeHashes      []string          `json:"node_hashes"`
	Filter          map[string]string `json:"filter"`
}

// Empty reports whether the scope selects nothing implicitly.
func (s Scope) Empty() bool {
	return !s.All && len(s.SubscriptionIDs) == 0 && len(s.PlatformIDs) == 0 &&
		len(s.NodeHashes) == 0 && len(s.Filter) == 0
}

// Request is the POST /api/v1/intel/jobs body.
type Request struct {
	Kind      Kind     `json:"kind"`
	Scope     Scope    `json:"scope"`
	Providers []string `json:"providers"`
	Checks    []string `json:"checks"`
	Force     bool     `json:"force"`
}

// Validate checks the request shape before any node expansion happens.
func (r Request) Validate() error {
	if _, err := ParseKind(string(r.Kind)); err != nil {
		return err
	}
	for _, provider := range r.Providers {
		if strings.TrimSpace(provider) == "" {
			return fmt.Errorf("%w: empty provider id", ErrInvalidJob)
		}
	}
	for _, check := range r.Checks {
		if strings.TrimSpace(check) == "" {
			return fmt.Errorf("%w: empty check id", ErrInvalidJob)
		}
	}
	return nil
}

// StepResult is the outcome of one pipeline step.
type StepResult struct {
	// DeferUntilNs > 0 returns the item to the queue with that next_run_at_ns
	// without recording a failure (budget or QPS gate closed, §3.2 step 4).
	DeferUntilNs int64
	// Skip marks the item as skipped: no further steps run.
	Skip bool
	// ErrorCode non-empty marks the step as failed.
	ErrorCode string
	// Summary is stored in job_items.result_json (bounded to 4 KiB).
	Summary map[string]any
}

// StepRunner executes one pipeline step for one node. WP09 (providers and
// checks) and WP10 (assessment) provide the implementations; WP08 only
// schedules them.
type StepRunner interface {
	RunStep(ctx context.Context, kind Kind, step Step, jobID, nodeHash string) StepResult
}

// StepRunnerFunc adapts a function to StepRunner.
type StepRunnerFunc func(ctx context.Context, kind Kind, step Step, jobID, nodeHash string) StepResult

// RunStep implements StepRunner.
func (f StepRunnerFunc) RunStep(ctx context.Context, kind Kind, step Step, jobID, nodeHash string) StepResult {
	return f(ctx, kind, step, jobID, nodeHash)
}

// ScopeResolver expands a job scope into node hashes. The expansion happens
// server-side and is a union of every selector (§3.1).
type ScopeResolver interface {
	ResolveScope(ctx context.Context, scope Scope) ([]string, error)
}

// ScopeResolverFunc adapts a function to ScopeResolver.
type ScopeResolverFunc func(ctx context.Context, scope Scope) ([]string, error)

// ResolveScope implements ScopeResolver.
func (f ScopeResolverFunc) ResolveScope(ctx context.Context, scope Scope) ([]string, error) {
	return f(ctx, scope)
}

// Clock is injectable so tests never sleep.
type Clock interface{ Now() time.Time }

// SystemClock is the wall clock used in production.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// Config is the hot-reloadable slice of the runtime configuration the executor
// needs (§3.4).
type Config struct {
	Enabled                  bool
	NodeWorkers              int
	MaxRunningJobs           int
	CheckConcurrencyPerCheck int
}

// Normalize clamps the configuration to the documented bounds (R4/R5).
func (c Config) Normalize() Config {
	if c.NodeWorkers <= 0 {
		c.NodeWorkers = DefaultNodeWorkers
	}
	if c.NodeWorkers > MaxNodeWorkers {
		c.NodeWorkers = MaxNodeWorkers
	}
	if c.MaxRunningJobs <= 0 {
		c.MaxRunningJobs = DefaultMaxRunningJobs
	}
	if c.CheckConcurrencyPerCheck <= 0 {
		c.CheckConcurrencyPerCheck = DefaultCheckConcurrencyPerCheck
	}
	return c
}

// ConfigProvider returns the current configuration snapshot.
type ConfigProvider func() Config

// CreatedBy records who asked for a job (jobs.created_by).
func CreatedByAdmin() string { return "admin" }

// CreatedBySubscription is the created_by value of an automatic subscription job.
func CreatedBySubscription(id string) string { return "system:subscription:" + id }

// CreatedByRefresh is the created_by value of the scheduled refresh job.
func CreatedByRefresh() string { return "system:refresh" }

// backoff returns the exponential retry delay of §3.3: 30s * 2^attempts capped
// at six hours.
func backoff(attempts int) time.Duration {
	delay := backoffBase
	for i := 0; i < attempts; i++ {
		delay *= 2
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// itemStatusIsTerminal reports whether an item status ends the item's life.
func itemStatusIsTerminal(status string) bool {
	switch status {
	case store.ItemDone, store.ItemFailed, store.ItemSkipped, store.ItemCanceled:
		return true
	default:
		return false
	}
}

// SubscriptionKindOf maps an intel job request to the job kind recorded for a
// subscription or refresh trigger (§3.6).
func SubscriptionKindOf(autoChecks bool) Kind {
	if autoChecks {
		return KindFull
	}
	return KindIntel
}

// RefreshedNodeFilter is the subset of the node list query parameters the
// scheduled refresh job maps onto model quality filters (WP10 §4).
const RefreshedNodeFilterKey = "filter"

// Ensure model stays referenced for WP09/WP10 callers that build scopes from
// node models.
var _ = model.QualityPolicy{}
