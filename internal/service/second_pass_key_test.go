package service

import (
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"prism/internal/model"
)

type pausedCredentialRandom struct {
	entered, release chan struct{}
	once             sync.Once
}

func (r *pausedCredentialRandom) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.release })
	return rand.Read(p)
}

func TestConcurrentKeyCreationCannotRestoreDisabledOwner(t *testing.T) {
	cp, store, _, _ := newTenantTestService(t)
	now := time.Now().UnixNano()
	plan, err := cp.CreatePlan(PlanInput{Name: "key-race-plan", Period: "30d", DurationDays: 30, Status: "active"}, now)
	if err != nil {
		t.Fatal(err)
	}
	user, err := cp.CreateTenantUser(TenantUserInput{Username: "key-race-user", Password: "key-race-password", Role: model.UserRoleUser, PlanID: plan.ID, DurationDays: 30}, now)
	if err != nil {
		t.Fatal(err)
	}
	// UUID generation occurs after CreateAccessKey has read its owner/plan.
	// Pause there while the administrator attempts to disable the owner.
	random := &pausedCredentialRandom{entered: make(chan struct{}), release: make(chan struct{})}
	uuid.SetRand(random)
	defer uuid.SetRand(nil)
	type result struct {
		key AccessKeyView
		err error
	}
	created := make(chan result, 1)
	go func() {
		key, _, err := cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "racing-key"}, now+1)
		created <- result{key, err}
	}()
	<-random.entered
	disabled := make(chan error, 1)
	go func() {
		status := model.UserStatusDisabled
		_, err := cp.UpdateTenantUser(user.ID, TenantUserPatch{Status: &status}, now+2)
		disabled <- err
	}()
	var disableErr error
	completed := false
	select {
	case disableErr = <-disabled:
		completed = true
	case <-time.After(50 * time.Millisecond):
	}
	close(random.release)
	got := <-created
	if !completed {
		disableErr = <-disabled
	}
	if got.err != nil || disableErr != nil {
		t.Fatalf("create=%v disable=%v", got.err, disableErr)
	}
	if live, ok := store.Get(got.key.ID); ok && live.UserStatus != model.UserStatusDisabled {
		t.Fatalf("disabled owner republished active: %+v", live)
	}
}
