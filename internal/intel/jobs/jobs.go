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
		// Wrap the sentinel so an unknown kind is an invalid argument (400), not
		// an internal error (500): every other shape failure in Request.Validate
		// already wraps it.
		return "", fmt.Errorf("%w: unknown job kind %q", ErrInvalidJob, raw)
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
	// itemTimeout bounds one item's whole pipeline run, every step included. It
	// has to stay above the sum of the unlock-check rule timeouts (94s for the
	// eight built-ins): a full pipeline used to be cut off at 90s, in the middle
	// of the check step, and the rules that never got to run were reported as
	// errors of the node.
	itemTimeout = 180 * time.Second
	// itemLease protects a claim from being handed to a second worker. It must
	// stay above itemTimeout, otherwise a lease could expire while its owner is
	// still running the item and two workers would run the same node at once.
	itemLease = 5 * time.Minute
	// jobStallYield is how long a running job may settle no item before the
	// scheduler sorts it behind the queued jobs. A running job keeps its slot by
	// default (§3.4) so a newly created job queues behind in-flight work instead
	// of preempting it - but a job whose items only ever get parked (a closed
	// provider gate, a via-node budget that never reopens) would then hold that
	// slot forever, which is what made every later job wait on 2026-10-02.
	// Yielding is a sort order, not a state change: the stalled job keeps running
	// whenever no queued job wants the slot.
	jobStallYield = 30 * time.Minute
	// jobStallTimeout is when a stalled job is given up on: no item settled for
	// this long *while another job waits for a slot* fails its pending items and
	// settles it as partial, so the job reaches a terminal state instead of
	// lingering as running forever.
	//
	// It is a no-progress bound, not a wall-clock one, and it is only applied
	// under queued pressure. A wall-clock bound cannot work here: a legitimately
	// retrying item sleeps up to backoffMax (6h) between attempts and gets
	// maxItemAttempts of them, so an honest job can spend a day without settling
	// anything, and a 200k-item job has no meaningful wall-clock budget at all.
	// Killing those would destroy collected evidence for no gain - while a job
	// nobody is waiting on costs nothing, because jobStallYield already took it
	// out of the slot window.
	jobStallTimeout     = 12 * time.Hour
	providerLease       = 2 * time.Minute
	maxItemAttempts     = 5
	maxProviderAttempts = 5
	backoffBase         = 30 * time.Second
	backoffMax          = 6 * time.Hour
	// deferFloorMax caps the retry floor a parked item gets in deferDeadline. It
	// has to mirror the pipeline's own deferral bound (internal/intel's
	// DefaultViaNodeDeferral, 30 minutes): step 4 only parks an item when the gate
	// reopens inside that bound, so a floor beyond it would hold the item longer
	// than the step ever agreed to wait, with its job still holding a
	// max_running_jobs slot.
	deferFloorMax    = 30 * time.Minute
	defaultTick      = time.Second
	idlePollInterval = 250 * time.Millisecond
	// maxActiveJobsFetched bounds one scheduling pass. It stays well above
	// MaxRunningJobs so a burst of queued jobs cannot push the running ones out
	// of the fetched window and stall them behind the truncation.
	maxActiveJobsFetched = 4 * MaxRunningJobs
)

// Default queue and pool bounds (R4).
const (
	// DefaultNodeWorkers is the default parallelism of the node pipeline.
	DefaultNodeWorkers = 16
	// DefaultMaxRunningJobs is the default number of jobs allowed to execute.
	DefaultMaxRunningJobs = 2
	// DefaultCheckConcurrencyPerCheck is the fallback global concurrency of one
	// unlock check rule, used when a caller supplies no value. It mirrors
	// checks.DefaultConcurrencyPerCheck; the jobs package keeps its own copy to
	// avoid importing the check engine (and sing-box with it) for one integer.
	DefaultCheckConcurrencyPerCheck = 512
	// MaxCheckConcurrencyPerCheck is the validated upper bound of
	// intel_check_concurrency_per_check. The engine allocates a semaphore of that
	// size per rule, so an unbounded value would let one config entry allocate
	// unbounded memory.
	MaxCheckConcurrencyPerCheck = 4096
	// MaxRunningJobs is the validated upper bound of intel_max_running_jobs.
	// Every running job holds one entry in the active set and competes for the
	// same node worker pool, so the ceiling exists to keep the scheduling pass
	// and its per-job progress bookkeeping bounded.
	MaxRunningJobs = 1024
	// MaxNodeWorkers is the validated upper bound of intel_node_workers.
	//
	// Each in-flight item works on one node, and the requests leave through that
	// node, so the pool is bounded by memory and file descriptors rather than by
	// any vendor rate: a node's own limits still apply to that node alone.
	MaxNodeWorkers = 512
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

// Item error codes written by the scheduler itself (step runners use their own).
const (
	// CodeItemTimeout marks an item whose pipeline ran out of its itemTimeout
	// budget while a step was still executing.
	CodeItemTimeout = "ITEM_TIMEOUT"
	// CodeJobTimeout marks an item failed by the job-level timeout sweep of
	// §3.4: the job as a whole ran past jobTimeout and its pending items were
	// failed so the job stops holding a max_running_jobs slot.
	CodeJobTimeout = "JOB_TIMEOUT"
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

// CreatedByAdmin is the created_by value of a job a human asked for
// (jobs.created_by).
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

// SubscriptionKindOf maps an intel job request to the job kind recorded for a
// subscription or refresh trigger (§3.6).
func SubscriptionKindOf(autoChecks bool) Kind {
	if autoChecks {
		return KindFull
	}
	return KindIntel
}

// Ensure model stays referenced for WP09/WP10 callers that build scopes from
// node models.
var _ = model.QualityPolicy{}
