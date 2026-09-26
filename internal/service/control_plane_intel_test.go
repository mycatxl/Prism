package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"prism/internal/intel"
	"prism/internal/intel/jobs"
	"prism/internal/intel/store"
	"prism/internal/node"
)

// WP08 §3: the batch job surface of internal/service. internal/api has no
// handler test for /api/v1/intel/jobs, so every branch below (the sentinel
// mapping, the pagination/status normalisation, cancel and the SSE
// subscription) is real coverage instead of a duplicate of an API test.

// newIntelJobFixture wires a job executor with a controllable intel_enabled
// flag. The manager is never started, so no worker runs and no test needs to
// synchronise with the pipeline.
func newIntelJobFixture(t *testing.T, enabled bool) (*ControlPlaneService, *intel.Service) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state", store.FileName))
	if err != nil {
		t.Fatalf("open intel store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc, err := intel.NewService(intel.Options{
		Store: st,
		Scope: jobs.ScopeResolverFunc(func(_ context.Context, scope jobs.Scope) ([]string, error) {
			return scope.NodeHashes, nil
		}),
		Config: func() jobs.Config {
			return jobs.Config{Enabled: enabled, NodeWorkers: 1, MaxRunningJobs: 1}
		},
		Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("intel.NewService: %v", err)
	}
	t.Cleanup(svc.Stop)
	return &ControlPlaneService{Intel: svc}, svc
}

func intelJobTestHash(server string) string {
	return node.HashFromRawOptions([]byte(`{"type":"ss","server":"` + server + `","port":443}`)).Hex()
}

func TestMapIntelError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    string
		message string
	}{
		{name: "nil stays nil", err: nil},
		{name: "disabled", err: jobs.ErrDisabled, want: "CONFLICT", message: "intel jobs are disabled (intel_enabled=false)"},
		{name: "wrapped disabled", err: fmt.Errorf("create job: %w", jobs.ErrDisabled), want: "CONFLICT", message: "intel jobs are disabled (intel_enabled=false)"},
		{name: "too many nodes", err: fmt.Errorf("%w: 100 > 10", jobs.ErrTooManyNodes), want: "INVALID_ARGUMENT"},
		{name: "invalid job", err: fmt.Errorf("%w: empty provider id", jobs.ErrInvalidJob), want: "INVALID_ARGUMENT"},
		{name: "too many subscribers", err: jobs.ErrTooManySubscribers, want: "RATE_LIMITED"},
		{name: "unknown job", err: store.ErrJobNotFound, want: "NOT_FOUND", message: "job not found"},
		{name: "anything else", err: errors.New("boom"), want: "INTERNAL", message: "intel job"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapped := mapIntelError(tt.err)
			if tt.want == "" {
				if mapped != nil {
					t.Fatalf("mapIntelError(nil) = %v, want nil", mapped)
				}
				return
			}
			if !isServiceErrorCode(mapped, tt.want) {
				t.Fatalf("mapIntelError(%v) = %v, want code %s", tt.err, mapped, tt.want)
			}
			if tt.message != "" && mapped.Error() != tt.message {
				t.Errorf("message = %q, want %q", mapped.Error(), tt.message)
			}
		})
	}
}

func TestIntelJobsWithoutWiring(t *testing.T) {
	empty := &ControlPlaneService{}
	ctx := context.Background()

	calls := map[string]func() error{
		"CreateIntelJob": func() error {
			_, err := empty.CreateIntelJob(ctx, IntelJobRequest{Kind: jobs.KindIntel})
			return err
		},
		"ListIntelJobs": func() error {
			_, _, err := empty.ListIntelJobs(ctx, "", 10, 0)
			return err
		},
		"GetIntelJob": func() error {
			_, err := empty.GetIntelJob(ctx, "any")
			return err
		},
		"ListIntelJobItems": func() error {
			_, err := empty.ListIntelJobItems(ctx, "any", "", 10, 0)
			return err
		},
		"CancelIntelJob": func() error {
			_, err := empty.CancelIntelJob(ctx, "any")
			return err
		},
		"RetryFailedIntelJob": func() error {
			_, _, err := empty.RetryFailedIntelJob(ctx, "any")
			return err
		},
		"SubscribeIntelJob": func() error {
			_, err := empty.SubscribeIntelJob(ctx, "any")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !isServiceErrorCode(err, "CONFLICT") {
				t.Fatalf("%s without the intel subsystem = %v, want CONFLICT", name, err)
			}
		})
	}
}

func TestIntelJobLifecycle(t *testing.T) {
	cp, svc := newIntelJobFixture(t, true)
	ctx := context.Background()

	first := intelJobTestHash("1.1.1.1")
	second := intelJobTestHash("2.2.2.2")

	job, err := cp.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.KindIntel,
		Scope: jobs.Scope{NodeHashes: []string{first, second}},
	})
	if err != nil {
		t.Fatalf("CreateIntelJob: %v", err)
	}
	if job.ID == "" || job.Status != store.JobQueued || job.Kind != string(jobs.KindIntel) {
		t.Fatalf("created job = %+v", job)
	}
	if job.CreatedBy != jobs.CreatedByAdmin() {
		t.Fatalf("created_by = %q, want %q", job.CreatedBy, jobs.CreatedByAdmin())
	}
	if job.Total != 2 {
		t.Fatalf("job total = %d, want 2", job.Total)
	}

	detail, err := cp.GetIntelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetIntelJob: %v", err)
	}
	if detail.Job.ID != job.ID || detail.Progress.Total != 2 || detail.Progress.Status != store.JobQueued {
		t.Fatalf("job detail = %+v", detail)
	}

	// A second job makes the paging of the list observable.
	if _, err := cp.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.KindFull,
		Scope: jobs.Scope{NodeHashes: []string{first}},
	}); err != nil {
		t.Fatalf("CreateIntelJob(second): %v", err)
	}

	all, total, err := cp.ListIntelJobs(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("ListIntelJobs: %v", err)
	}
	if total != 2 || len(all) != 2 {
		t.Fatalf("ListIntelJobs = %d items, total %d, want 2/2", len(all), total)
	}
	page, total, err := cp.ListIntelJobs(ctx, "", 1, 0)
	if err != nil {
		t.Fatalf("ListIntelJobs(page): %v", err)
	}
	if total != 2 || len(page) != 1 {
		t.Fatalf("ListIntelJobs(page) = %d items, total %d, want 1/2", len(page), total)
	}
	// An unknown status is not an error: the filter is simply dropped.
	if got, _, err := cp.ListIntelJobs(ctx, "not-a-status", 10, 0); err != nil || len(got) != 2 {
		t.Fatalf("ListIntelJobs(bogus status) = %d items, err %v, want 2 and nil", len(got), err)
	}
	if got, _, err := cp.ListIntelJobs(ctx, store.JobQueued, 10, 0); err != nil || len(got) != 2 {
		t.Fatalf("ListIntelJobs(queued) = %d items, err %v", len(got), err)
	}
	if got, _, err := cp.ListIntelJobs(ctx, store.JobCanceled, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("ListIntelJobs(canceled) = %d items, err %v, want none", len(got), err)
	}

	items, err := cp.ListIntelJobItems(ctx, job.ID, "", 10, 0)
	if err != nil {
		t.Fatalf("ListIntelJobItems: %v", err)
	}
	if items.Total != 2 || len(items.Items) != 2 || items.JobID != job.ID || items.JobState != store.JobQueued {
		t.Fatalf("job items = %+v", items)
	}
	seen := map[string]bool{}
	for _, item := range items.Items {
		seen[item.NodeHash] = true
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("job items = %+v, want both node hashes", items.Items)
	}
	// An unknown item status is dropped the same way.
	if filtered, err := cp.ListIntelJobItems(ctx, job.ID, "not-a-status", 10, 0); err != nil || filtered.Total != 2 {
		t.Fatalf("ListIntelJobItems(bogus status) = %+v, err %v", filtered, err)
	}
	if filtered, err := cp.ListIntelJobItems(ctx, job.ID, store.ItemQueued, 10, 0); err != nil || filtered.Total != 2 {
		t.Fatalf("ListIntelJobItems(queued) = %+v, err %v", filtered, err)
	}
	if filtered, err := cp.ListIntelJobItems(ctx, job.ID, store.ItemFailed, 10, 0); err != nil || filtered.Total != 0 {
		t.Fatalf("ListIntelJobItems(failed) = %+v, err %v, want none", filtered, err)
	}

	// No item failed, so the retry is a no-op that still returns the job.
	retried, count, err := cp.RetryFailedIntelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("RetryFailedIntelJob: %v", err)
	}
	if count != 0 || retried.ID != job.ID {
		t.Fatalf("RetryFailedIntelJob = %d retried, job %+v", count, retried)
	}

	sub, err := cp.SubscribeIntelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("SubscribeIntelJob: %v", err)
	}
	sub.Close()

	canceled, err := cp.CancelIntelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("CancelIntelJob: %v", err)
	}
	if canceled.ID != job.ID || canceled.Status != store.JobCanceled {
		t.Fatalf("canceled job = %+v, want status %s", canceled, store.JobCanceled)
	}
	stored, err := svc.Manager().Store().GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if stored.Status != store.JobCanceled {
		t.Fatalf("persisted job status = %q, want %q", stored.Status, store.JobCanceled)
	}
}

func TestIntelJobUnknownIDs(t *testing.T) {
	cp, _ := newIntelJobFixture(t, true)
	ctx := context.Background()

	const missing = "00000000-0000-0000-0000-000000000000"
	if _, err := cp.GetIntelJob(ctx, missing); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("GetIntelJob(unknown) = %v, want NOT_FOUND", err)
	}
	if _, err := cp.ListIntelJobItems(ctx, missing, "", 10, 0); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("ListIntelJobItems(unknown) = %v, want NOT_FOUND", err)
	}
	if _, err := cp.CancelIntelJob(ctx, missing); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("CancelIntelJob(unknown) = %v, want NOT_FOUND", err)
	}
	if _, _, err := cp.RetryFailedIntelJob(ctx, missing); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("RetryFailedIntelJob(unknown) = %v, want NOT_FOUND", err)
	}
	if _, err := cp.SubscribeIntelJob(ctx, missing); !isServiceErrorCode(err, "NOT_FOUND") {
		t.Fatalf("SubscribeIntelJob(unknown) = %v, want NOT_FOUND", err)
	}
}

func TestCreateIntelJobValidation(t *testing.T) {
	disabled, _ := newIntelJobFixture(t, false)
	ctx := context.Background()

	if _, err := disabled.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.KindIntel,
		Scope: jobs.Scope{NodeHashes: []string{intelJobTestHash("3.3.3.3")}},
	}); !isServiceErrorCode(err, "CONFLICT") {
		t.Fatalf("CreateIntelJob with intel_enabled=false = %v, want CONFLICT", err)
	}

	enabled, _ := newIntelJobFixture(t, true)
	// An unknown job kind is a client mistake, so it must map to 400:
	// jobs.ParseKind now wraps jobs.ErrInvalidJob like every other shape failure
	// in Request.Validate. (Previously it returned a bare error, which fell
	// through mapIntelError to INTERNAL and answered HTTP 500.)
	if _, err := enabled.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.Kind("not-a-kind"),
		Scope: jobs.Scope{NodeHashes: []string{intelJobTestHash("4.4.4.4")}},
	}); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("CreateIntelJob with an unknown kind = %v, want INVALID_ARGUMENT", err)
	}
	// A wrapping sentinel is a client error and is mapped correctly.
	if _, err := enabled.CreateIntelJob(ctx, IntelJobRequest{
		Kind:      jobs.KindIntel,
		Providers: []string{"  "},
		Scope:     jobs.Scope{NodeHashes: []string{intelJobTestHash("5.5.5.5")}},
	}); !isServiceErrorCode(err, "INVALID_ARGUMENT") {
		t.Fatalf("CreateIntelJob with an empty provider id = %v, want INVALID_ARGUMENT", err)
	}
	// A scope that expands to nothing settles immediately instead of failing.
	job, err := enabled.CreateIntelJob(ctx, IntelJobRequest{Kind: jobs.KindIntel})
	if err != nil {
		t.Fatalf("CreateIntelJob(empty scope): %v", err)
	}
	if job.ID == "" || job.Total != 0 {
		t.Fatalf("empty-scope job = %+v", job)
	}
}

// A broken intel.db is an INTERNAL error, not an empty job list.
func TestListIntelJobsStoreFailure(t *testing.T) {
	cp, svc := newIntelJobFixture(t, true)
	ctx := context.Background()

	if err := svc.Store().Close(); err != nil {
		t.Fatalf("close intel store: %v", err)
	}
	if _, _, err := cp.ListIntelJobs(ctx, "", 10, 0); !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("ListIntelJobs with a closed store = %v, want INTERNAL", err)
	}
	if _, err := cp.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.KindIntel,
		Scope: jobs.Scope{NodeHashes: []string{intelJobTestHash("6.6.6.6")}},
	}); !isServiceErrorCode(err, "INTERNAL") {
		t.Fatalf("CreateIntelJob with a closed store = %v, want INTERNAL", err)
	}
}

// Over the documented SSE fan-out limit the subscription is rate limited
// instead of blocking the executor.
func TestSubscribeIntelJobRejectsTooManySubscribers(t *testing.T) {
	cp, _ := newIntelJobFixture(t, true)
	ctx := context.Background()

	job, err := cp.CreateIntelJob(ctx, IntelJobRequest{
		Kind:  jobs.KindIntel,
		Scope: jobs.Scope{NodeHashes: []string{intelJobTestHash("7.7.7.7")}},
	})
	if err != nil {
		t.Fatalf("CreateIntelJob: %v", err)
	}

	subs := make([]*jobs.Subscription, 0, jobs.SSEMaxSubscribers)
	for i := 0; i < jobs.SSEMaxSubscribers; i++ {
		sub, err := cp.SubscribeIntelJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("SubscribeIntelJob(%d): %v", i, err)
		}
		subs = append(subs, sub)
	}
	defer func() {
		for _, sub := range subs {
			sub.Close()
		}
	}()

	if _, err := cp.SubscribeIntelJob(ctx, job.ID); !isServiceErrorCode(err, "RATE_LIMITED") {
		t.Fatalf("SubscribeIntelJob over the limit = %v, want RATE_LIMITED", err)
	}
}
