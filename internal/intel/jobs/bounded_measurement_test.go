package jobs

import (
	"context"
	"prism/internal/intel/store"
	"testing"
	"time"
)

func TestTimeoutAndProviderWaitHaveTerminalBounds(t *testing.T) {
	for _, code := range []string{CodeItemTimeout, CodeScopeValidationError, "BREAKPOINT_WRITE_FAILED", "PROVIDER_WAIT_EXHAUSTED"} {
		t.Run(code, func(t *testing.T) {
			st := newTestStore(t)
			ctx := context.Background()
			clock := newFakeClock(time.Now())
			runner := &scriptedRunner{handlers: map[Step]StepResult{StepEgress: {DeferUntilNs: clock.Now().Add(time.Minute).UnixNano()}}}
			scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"bounded"}, nil })
			m := newTestManager(t, st, clock, runner, scope, nil)
			job, err := m.Create(ctx, Request{Kind: KindEgress}, CreatedByAdmin(), PriorityManual)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < maxItemAttempts; i++ {
				m.refreshActiveJobs(m.Config())
				item, ok, err := m.claimOne(ctx)
				if err != nil || !ok {
					t.Fatal(i, ok, err)
				}
				if code == "PROVIDER_WAIT_EXHAUSTED" {
					m.runItem(ctx, item)
				} else {
					m.retryOrFail(ctx, item, 0, code, map[string]any{"attempted_step": "egress"})
				}
				clock.advance(10 * time.Minute)
			}
			item, _, err := st.GetJobItem(ctx, job.ID, "bounded")
			if err != nil || item.Status != store.ItemFailed || item.Attempts != maxItemAttempts || item.ErrorCode != code {
				t.Fatal(item, err)
			}
			m.refreshActiveJobs(m.Config())
			if _, ok, _ := m.claimOne(ctx); ok {
				t.Fatal("terminal item was reclaimed")
			}
		})
	}
}

func TestProviderFailuresRetryThenPreserveUsefulChecksAndAssessments(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	clock := newFakeClock(time.Now())
	checks, assess, calls := 0, 0, 0
	runner := StepRunnerFunc(func(_ context.Context, _ Kind, step Step, _, _ string) StepResult {
		switch step {
		case StepViaNode:
			calls++
			return StepResult{ErrorCode: "PROVIDER_FAILED", ContinueAfterRetries: true, Summary: map[string]any{"provider_failed": true}}
		case StepChecks:
			checks++
		case StepAssess:
			assess++
		}
		return StepResult{}
	})
	scope := ScopeResolverFunc(func(context.Context, Scope) ([]string, error) { return []string{"partial"}, nil })
	m := newTestManager(t, st, clock, runner, scope, nil)
	job, err := m.Create(ctx, Request{Kind: KindFull}, CreatedByAdmin(), PriorityManual)
	if err != nil {
		t.Fatal(err)
	}
	for range maxItemAttempts {
		m.refreshActiveJobs(m.Config())
		item, ok, err := m.claimOne(ctx)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		m.runItem(ctx, item)
		clock.advance(10 * time.Minute)
	}
	item, _, _ := st.GetJobItem(ctx, job.ID, "partial")
	if calls != 5 || checks != 1 || assess != 1 || item.Status != store.ItemFailed || item.ErrorCode != "PROVIDER_FAILED" {
		t.Fatal(calls, checks, assess, item)
	}
}
