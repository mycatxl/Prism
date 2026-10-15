package service

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/config"
	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/state"
)

func newTenantTestService(t *testing.T) (*ControlPlaneService, *keys.Store, *auth.Pepper, int64) {
	t.Helper()
	root := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	rawPepper, err := auth.GeneratePepper()
	if err != nil {
		t.Fatalf("GeneratePepper: %v", err)
	}
	pepper, err := auth.NewPepper(rawPepper)
	if err != nil {
		t.Fatalf("NewPepper: %v", err)
	}

	keyStore := keys.NewStore()
	nowNs := int64(1700000000) * int64(time.Second)

	cp := &ControlPlaneService{
		Engine: engine,
		Keys:   keyStore,
		Pepper: pepper,
	}
	return cp, keyStore, pepper, nowNs
}

func TestPlanCRUD_LifecycleAndValidation(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	// 1. CreatePlan
	input := PlanInput{
		Name:            "Standard Tier",
		Platforms:       []string{"plt_1"},
		IPTiers:         []string{"residential:clean"},
		TrafficBytes:    10 * 1024 * 1024,
		Period:          "30d",
		MaxConcurrent:   5,
		MaxRPS:          10,
		MaxBandwidthBps: 1000000,
		MaxKeys:         3,
		DurationDays:    30,
		Status:          "active",
	}

	created, err := cp.CreatePlan(input, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan failed: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected non-empty plan ID")
	}
	if created.ActiveUserCount != 0 {
		t.Fatalf("expected active_user_count 0, got %d", created.ActiveUserCount)
	}
	if created.Name != input.Name || created.TrafficBytes != input.TrafficBytes {
		t.Fatalf("created plan fields mismatched: %+v", created)
	}

	// 2. ListPlans shows active_user_count 0
	plans, err := cp.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans failed: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if plans[0].ID != created.ID || plans[0].ActiveUserCount != 0 {
		t.Fatalf("ListPlans entry unexpected: %+v", plans[0])
	}

	// 3. GetPlan
	got, err := cp.GetPlan(created.ID)
	if err != nil {
		t.Fatalf("GetPlan failed: %v", err)
	}
	if got.ID != created.ID || got.Name != input.Name || got.ActiveUserCount != 0 {
		t.Fatalf("GetPlan mismatched: %+v", got)
	}

	// 4. PATCH name/traffic_bytes/status
	patchJSON := []byte(`{"name":"Standard Plus","traffic_bytes":20971520,"status":"disabled"}`)
	updated, err := cp.UpdatePlan(created.ID, patchJSON, nowNs)
	if err != nil {
		t.Fatalf("UpdatePlan failed: %v", err)
	}
	if updated.Name != "Standard Plus" || updated.TrafficBytes != 20971520 || updated.Status != "disabled" {
		t.Fatalf("UpdatePlan patch not applied: %+v", updated)
	}

	// 5. PATCH an unknown field returns *ServiceError Code INVALID_ARGUMENT
	_, err = cp.UpdatePlan(created.ID, []byte(`{"unknown_field":"invalid"}`), nowNs)
	if err == nil {
		t.Fatal("expected error patching unknown field, got nil")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("expected INVALID_ARGUMENT for unknown field, got %v", err)
	}

	// 6. PATCH with an invalid ip_tiers entry (e.g. ["no-such-type:clean"]) is refused
	_, err = cp.UpdatePlan(created.ID, []byte(`{"ip_tiers":["no-such-type:clean"]}`), nowNs)
	if err == nil {
		t.Fatal("expected error patching invalid ip_tiers, got nil")
	}
	if !errors.As(err, &svcErr) || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("expected INVALID_ARGUMENT for invalid ip_tiers, got %v", err)
	}

	// 7. Delete a plan that a user is on and confirm the subscription survives with an empty plan_id
	// Re-enable plan first so subscription can be assigned
	_, err = cp.UpdatePlan(created.ID, []byte(`{"status":"active"}`), nowNs)
	if err != nil {
		t.Fatalf("re-enabling plan failed: %v", err)
	}

	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "alice_plan_del",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       created.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser failed: %v", err)
	}
	if user.Subscription == nil || user.Subscription.PlanID != created.ID {
		t.Fatalf("expected subscription on plan %s, got %+v", created.ID, user.Subscription)
	}
	subID := user.Subscription.ID

	if err := cp.DeletePlan(created.ID); err != nil {
		t.Fatalf("DeletePlan failed: %v", err)
	}

	// Subscription survives with empty plan_id
	subView, err := cp.GetTenantSubscription(subID)
	if err != nil {
		t.Fatalf("GetTenantSubscription after plan deletion failed: %v", err)
	}
	if subView.ID != subID {
		t.Fatalf("subscription ID mismatched: got %s, want %s", subView.ID, subID)
	}
	if subView.PlanID != "" {
		t.Fatalf("expected empty plan_id after plan deletion, got %q", subView.PlanID)
	}
}

func TestPlanCRUD_ApplyToExisting(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	// 1. Create plan with TrafficBytes: 1000
	plan, err := cp.CreatePlan(PlanInput{
		Name:         "Plan Snap Test",
		TrafficBytes: 1000,
		DurationDays: 30,
		Status:       "active",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	// 2. Put user on the plan
	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "snap_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	sub, err := cp.Engine.GetActiveUserSubscription(user.ID)
	if err != nil {
		t.Fatalf("GetActiveUserSubscription: %v", err)
	}
	initialSnap, err := planFromSnapshotJSON(sub.PlanSnapshotJSON)
	if err != nil {
		t.Fatalf("planFromSnapshotJSON: %v", err)
	}
	if initialSnap.TrafficBytes != 1000 {
		t.Fatalf("expected initial snapshot traffic_bytes 1000, got %d", initialSnap.TrafficBytes)
	}

	// 3. PATCH traffic_bytes WITHOUT apply_to_existing leaves user snapshot unchanged
	_, err = cp.UpdatePlan(plan.ID, []byte(`{"traffic_bytes": 2000}`), nowNs)
	if err != nil {
		t.Fatalf("UpdatePlan without apply_to_existing: %v", err)
	}

	subAfterWithout, err := cp.Engine.GetActiveUserSubscription(user.ID)
	if err != nil {
		t.Fatalf("GetActiveUserSubscription: %v", err)
	}
	snapWithout, err := planFromSnapshotJSON(subAfterWithout.PlanSnapshotJSON)
	if err != nil {
		t.Fatalf("planFromSnapshotJSON: %v", err)
	}
	if snapWithout.TrafficBytes != 1000 {
		t.Fatalf("expected snapshot traffic_bytes to remain 1000 without apply_to_existing, got %d", snapWithout.TrafficBytes)
	}

	// 4. PATCH traffic_bytes WITH apply_to_existing=true changes user snapshot
	_, err = cp.UpdatePlan(plan.ID, []byte(`{"traffic_bytes": 3000, "apply_to_existing": true}`), nowNs)
	if err != nil {
		t.Fatalf("UpdatePlan with apply_to_existing: %v", err)
	}

	subAfterWith, err := cp.Engine.GetActiveUserSubscription(user.ID)
	if err != nil {
		t.Fatalf("GetActiveUserSubscription: %v", err)
	}
	snapWith, err := planFromSnapshotJSON(subAfterWith.PlanSnapshotJSON)
	if err != nil {
		t.Fatalf("planFromSnapshotJSON: %v", err)
	}
	if snapWith.TrafficBytes != 3000 {
		t.Fatalf("expected snapshot traffic_bytes to update to 3000 with apply_to_existing=true, got %d", snapWith.TrafficBytes)
	}
}

func TestTenantUser_LifecycleAndSessions(t *testing.T) {
	cp, keyStore, _, nowNs := newTenantTestService(t)

	plan, err := cp.CreatePlan(PlanInput{
		Name:         "User Lifecycle Plan",
		DurationDays: 30,
		Status:       "active",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	// 1. CreateTenantUser
	detail, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "alice_lifecycle",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}
	if detail.Subscription == nil || detail.Subscription.PlanName != plan.Name {
		t.Fatalf("expected subscription with plan name %q, got %+v", plan.Name, detail.Subscription)
	}
	if detail.KeyCount != 0 || len(detail.Keys) != 0 {
		t.Fatalf("expected 0 keys initially, got key_count=%d, len(keys)=%d", detail.KeyCount, len(detail.Keys))
	}
	if detail.Role != "user" {
		t.Fatalf("expected role 'user', got %s", detail.Role)
	}

	// 2. Verify password actually works
	userRow, err := cp.Engine.GetUser(detail.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if err := auth.VerifyPassword(userRow.PasswordHash, "password-123456"); err != nil {
		t.Fatalf("VerifyPassword with right password failed: %v", err)
	}
	if err := auth.VerifyPassword(userRow.PasswordHash, "wrong-password"); err == nil {
		t.Fatal("VerifyPassword with wrong password unexpectedly succeeded")
	}

	// 3. SetTenantUserStatus("disabled") ends sessions
	rawSession1 := "raw-session-alice-1"
	sess1 := model.Session{
		IDHash:      auth.HashSessionID(rawSession1),
		UserID:      detail.ID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + int64(12*time.Hour),
		IP:          "127.0.0.1",
		UA:          "TestAgent",
	}
	if err := cp.Engine.InsertSession(sess1); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	sessions, err := cp.Engine.ListSessionsByUser(detail.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("expected 1 session before disable, got %d (err: %v)", len(sessions), err)
	}

	if _, err := cp.SetTenantUserStatus(detail.ID, "disabled", nowNs); err != nil {
		t.Fatalf("SetTenantUserStatus(disabled): %v", err)
	}
	sessionsAfterDisable, err := cp.Engine.ListSessionsByUser(detail.ID)
	if err != nil {
		t.Fatalf("ListSessionsByUser: %v", err)
	}
	if len(sessionsAfterDisable) != 0 {
		t.Fatalf("expected 0 sessions after user disabled, got %d", len(sessionsAfterDisable))
	}

	// Re-enable user
	if _, err := cp.SetTenantUserStatus(detail.ID, "active", nowNs); err != nil {
		t.Fatalf("SetTenantUserStatus(active): %v", err)
	}

	// 4. SetTenantUserPassword ends sessions
	rawSession2 := "raw-session-alice-2"
	sess2 := model.Session{
		IDHash:      auth.HashSessionID(rawSession2),
		UserID:      detail.ID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + int64(12*time.Hour),
		IP:          "127.0.0.1",
		UA:          "TestAgent",
	}
	if err := cp.Engine.InsertSession(sess2); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}
	if err := cp.SetTenantUserPassword(detail.ID, "new-password-123456", nowNs); err != nil {
		t.Fatalf("SetTenantUserPassword: %v", err)
	}
	sessionsAfterPW, err := cp.Engine.ListSessionsByUser(detail.ID)
	if err != nil {
		t.Fatalf("ListSessionsByUser: %v", err)
	}
	if len(sessionsAfterPW) != 0 {
		t.Fatalf("expected 0 sessions after password change, got %d", len(sessionsAfterPW))
	}
	userRowAfterPW, _ := cp.Engine.GetUser(detail.ID)
	if err := auth.VerifyPassword(userRowAfterPW.PasswordHash, "new-password-123456"); err != nil {
		t.Fatalf("VerifyPassword with new password failed: %v", err)
	}

	// 5. DeleteTenantUser removes user and its access keys from keys store
	keyView, _, err := cp.CreateAccessKey(detail.ID, AccessKeyInput{Name: "alice-to-delete"}, nowNs)
	if err != nil {
		t.Fatalf("CreateAccessKey: %v", err)
	}
	if _, ok := keyStore.Get(keyView.ID); !ok {
		t.Fatal("expected key to exist in live store before user deletion")
	}

	if err := cp.DeleteTenantUser(detail.ID); err != nil {
		t.Fatalf("DeleteTenantUser failed: %v", err)
	}

	// Assert user is gone
	if _, err := cp.Engine.GetUser(detail.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for deleted user, got %v", err)
	}
	// Assert key is gone from live store
	if _, ok := keyStore.Get(keyView.ID); ok {
		t.Fatal("expected key to be removed from live store after DeleteTenantUser")
	}
}

func TestAccessKey_LifecycleAndPermissions(t *testing.T) {
	cp, keyStore, pepper, nowNs := newTenantTestService(t)

	// Plan allows max_keys: 1 and platforms: plt_1, plt_2
	plan, err := cp.CreatePlan(PlanInput{
		Name:         "Limited Keys Plan",
		Platforms:    []string{"plt_1", "plt_2"},
		MaxKeys:      1,
		DurationDays: 30,
		Status:       "active",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "key_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	// 1. CreateAccessKey returns plaintext credential with matching hint; plaintext is NOT in AccessKeyView
	keyView, cred, err := cp.CreateAccessKey(user.ID, AccessKeyInput{
		Name:      "primary-key",
		Platforms: []string{"plt_1"},
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateAccessKey: %v", err)
	}
	if cred == "" {
		t.Fatal("expected non-empty plaintext credential")
	}
	expectedHint := keys.CredentialPrefix + keyView.ID + "_…"
	if keyView.CredentialHint != expectedHint {
		t.Fatalf("credential hint mismatch: got %q, want %q", keyView.CredentialHint, expectedHint)
	}

	// Verify plaintext secret is not stored: verify against DB row SecretHash
	keyRow, err := cp.Engine.GetAccessKey(keyView.ID)
	if err != nil {
		t.Fatalf("GetAccessKey: %v", err)
	}
	parsedID, parsedSecret, ok := keys.ParseCredential(cred)
	if !ok || parsedID != keyView.ID {
		t.Fatalf("ParseCredential failed or mismatched: ok=%v, parsedID=%s", ok, parsedID)
	}
	if !pepper.VerifySecret(keyRow.SecretHash, parsedSecret) {
		t.Fatal("pepper.VerifySecret failed with parsed plaintext secret")
	}
	if pepper.VerifySecret(keyRow.SecretHash, "bogus-secret") {
		t.Fatal("pepper.VerifySecret unexpectedly succeeded with bogus secret")
	}

	// Verify struct AccessKeyView has no secret field (only CredentialHint)
	valType := reflect.TypeOf(keyView)
	for i := 0; i < valType.NumField(); i++ {
		field := valType.Field(i)
		if strings.ToLower(field.Name) == "secret" || strings.ToLower(field.Tag.Get("json")) == "secret" {
			t.Fatalf("AccessKeyView must not leak secret, found field: %s", field.Name)
		}
	}

	// 2. With cp.Keys set, key is usable immediately through keys.Store
	kState, ok := keyStore.Get(keyView.ID)
	if !ok {
		t.Fatal("expected key to be loaded in keys.Store immediately")
	}
	if !kState.Usable(nowNs) {
		t.Fatal("expected key to be usable immediately")
	}
	if !reflect.DeepEqual(kState.EffectivePlatforms(), []string{"plt_1"}) {
		t.Fatalf("expected effective platforms ['plt_1'], got %v", kState.EffectivePlatforms())
	}

	// 3. User status disabled makes key unusable
	if _, err := cp.SetTenantUserStatus(user.ID, "disabled", nowNs); err != nil {
		t.Fatalf("SetTenantUserStatus(disabled): %v", err)
	}
	kState, _ = keyStore.Get(keyView.ID)
	if kState.Usable(nowNs) {
		t.Fatal("expected key to be unusable when user is disabled")
	}

	// Re-enable user
	if _, err := cp.SetTenantUserStatus(user.ID, "active", nowNs); err != nil {
		t.Fatalf("SetTenantUserStatus(active): %v", err)
	}
	kState, _ = keyStore.Get(keyView.ID)
	if !kState.Usable(nowNs) {
		t.Fatal("expected key to be usable after user is re-enabled")
	}

	// 4. Plan with max_keys: 1 refuses the second key
	_, _, err = cp.CreateAccessKey(user.ID, AccessKeyInput{Name: "second-key"}, nowNs)
	if err == nil {
		t.Fatal("expected conflict creating second key exceeding max_keys=1")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "CONFLICT" {
		t.Fatalf("expected CONFLICT error for exceeding max_keys, got %v", err)
	}

	// 5. UpdateAccessKey narrowing platforms is visible in the store
	patchedKey, err := cp.UpdateAccessKey(user.ID, keyView.ID, []byte(`{"platforms":["plt_2"]}`), nowNs)
	if err != nil {
		t.Fatalf("UpdateAccessKey: %v", err)
	}
	if !reflect.DeepEqual(patchedKey.Platforms, []string{"plt_2"}) {
		t.Fatalf("expected updated platforms ['plt_2'], got %v", patchedKey.Platforms)
	}
	kState, _ = keyStore.Get(keyView.ID)
	if !reflect.DeepEqual(kState.EffectivePlatforms(), []string{"plt_2"}) {
		t.Fatalf("expected store effective platforms ['plt_2'], got %v", kState.EffectivePlatforms())
	}

	// 6. DeleteAccessKey removes it from the store
	if err := cp.DeleteAccessKey(user.ID, keyView.ID); err != nil {
		t.Fatalf("DeleteAccessKey: %v", err)
	}
	if _, ok := keyStore.Get(keyView.ID); ok {
		t.Fatal("expected key to be removed from keys.Store after DeleteAccessKey")
	}
}

func TestSubscription_LifecycleAndWindow(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	p1, err := cp.CreatePlan(PlanInput{Name: "P1", DurationDays: 10, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan P1: %v", err)
	}
	p2, err := cp.CreatePlan(PlanInput{Name: "P2", DurationDays: 20, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan P2: %v", err)
	}

	user, err := cp.CreateAccount("sub_user", "password-123456", "user", nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// 1. AssignSubscription opens initial subscription
	sub1, err := cp.AssignSubscription(user.ID, p1.ID, 10, nowNs)
	if err != nil {
		t.Fatalf("AssignSubscription sub1: %v", err)
	}
	if sub1.Status != model.SubscriptionStatusActive {
		t.Fatalf("expected sub1 status active, got %s", sub1.Status)
	}

	// 2. AssignSubscription expires previously active subscription
	sub2, err := cp.AssignSubscription(user.ID, p2.ID, 20, nowNs)
	if err != nil {
		t.Fatalf("AssignSubscription sub2: %v", err)
	}
	if sub2.Status != model.SubscriptionStatusActive {
		t.Fatalf("expected sub2 status active, got %s", sub2.Status)
	}

	oldSub1, err := cp.Engine.GetUserSubscription(sub1.ID)
	if err != nil {
		t.Fatalf("GetUserSubscription(sub1): %v", err)
	}
	if oldSub1.Status != model.SubscriptionStatusExpired {
		t.Fatalf("expected previous subscription to be expired, got %s", oldSub1.Status)
	}

	activeSubs, err := cp.Engine.ListActiveUserSubscriptions()
	if err != nil {
		t.Fatalf("ListActiveUserSubscriptions: %v", err)
	}
	userActiveCount := 0
	for _, as := range activeSubs {
		if as.UserID == user.ID {
			userActiveCount++
		}
	}
	if userActiveCount != 1 {
		t.Fatalf("expected exactly 1 active subscription for user, got %d", userActiveCount)
	}

	// 3. UpdateTenantSubscription extend_days adds days from max(now, current expiry)
	// Case A: now < expiry -> extends from current expiry
	extendedA, err := cp.UpdateTenantSubscription(sub2.ID, []byte(`{"extend_days":5}`), nowNs)
	if err != nil {
		t.Fatalf("UpdateTenantSubscription extend_days (future expiry): %v", err)
	}
	expectedExpiryA := sub2.ExpiresAtNs + int64(5*24*time.Hour)
	if extendedA.ExpiresAtNs != expectedExpiryA {
		t.Fatalf("extend_days from future expiry mismatch: got %d, want %d", extendedA.ExpiresAtNs, expectedExpiryA)
	}

	// Case B: now > expiry -> extends from now
	futureNow := extendedA.ExpiresAtNs + int64(10*24*time.Hour)
	extendedB, err := cp.UpdateTenantSubscription(sub2.ID, []byte(`{"extend_days":3}`), futureNow)
	if err != nil {
		t.Fatalf("UpdateTenantSubscription extend_days (past expiry): %v", err)
	}
	expectedExpiryB := futureNow + int64(3*24*time.Hour)
	if extendedB.ExpiresAtNs != expectedExpiryB {
		t.Fatalf("extend_days from nowNs (past expiry) mismatch: got %d, want %d", extendedB.ExpiresAtNs, expectedExpiryB)
	}

	// 4. PATCH status suspended
	suspended, err := cp.UpdateTenantSubscription(sub2.ID, []byte(`{"status":"suspended"}`), nowNs)
	if err != nil {
		t.Fatalf("UpdateTenantSubscription(suspended): %v", err)
	}
	if suspended.Status != model.SubscriptionStatusSuspended {
		t.Fatalf("expected status suspended, got %s", suspended.Status)
	}

	// 5. DeleteTenantSubscription removes the row
	if err := cp.DeleteTenantSubscription(sub2.ID); err != nil {
		t.Fatalf("DeleteTenantSubscription: %v", err)
	}
	_, err = cp.GetTenantSubscription(sub2.ID)
	if err == nil {
		t.Fatal("expected not found getting deleted subscription, got nil")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND service error, got %v", err)
	}
}

func TestInvite_LifecycleAndRegistration(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	plan, err := cp.CreatePlan(PlanInput{Name: "Invite Plan", DurationDays: 30, Status: "active"}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	// 1. CreateInvite produces code in documented shape (XXXX-XXXX, alphabet without confusable chars)
	inv, err := cp.CreateInvite(InviteInput{
		PlanID:        plan.ID,
		MaxUses:       1,
		ExpiresInDays: 7,
		Note:          "VIP invite",
	}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if len(inv.Code) != 9 || inv.Code[4] != '-' {
		t.Fatalf("expected code format XXXX-XXXX (len 9 with - at index 4), got %s", inv.Code)
	}
	for i, r := range inv.Code {
		if i == 4 {
			continue
		}
		if !strings.ContainsRune(inviteCodeAlphabet, r) {
			t.Fatalf("code character %q not in allowed alphabet %s", r, inviteCodeAlphabet)
		}
	}

	// 2. ListInvites reports computed status
	invList, total, err := cp.ListInvites(10, 0, nowNs)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if total != 1 || len(invList) != 1 || invList[0].Status != model.InviteStatusActive {
		t.Fatalf("ListInvites active status unexpected: total=%d, list=%+v", total, invList)
	}

	// ListInvites past expiry computes status as disabled
	invListPast, _, err := cp.ListInvites(10, 0, nowNs+int64(8*24*time.Hour))
	if err != nil {
		t.Fatalf("ListInvites past expiry: %v", err)
	}
	if invListPast[0].Status != model.InviteStatusDisabled {
		t.Fatalf("expected computed status disabled for expired invite, got %s", invListPast[0].Status)
	}

	// 3. RedeemInvite on unknown code, used-up code, and expired code all fail with IDENTICAL error messages
	// Unknown code:
	_, errUnknown := cp.RedeemInvite("ZZZZ-ZZZZ", nowNs)
	if errUnknown == nil {
		t.Fatal("expected error on unknown code")
	}

	// Expired code:
	expInv, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 5, ExpiresInDays: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite expired: %v", err)
	}
	_, errExpired := cp.RedeemInvite(expInv.Code, nowNs+int64(2*24*time.Hour))
	if errExpired == nil {
		t.Fatal("expected error on expired code")
	}

	// Used-up code:
	usedInv, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite used-up: %v", err)
	}
	if _, err := cp.RedeemInvite(usedInv.Code, nowNs); err != nil {
		t.Fatalf("first redemption should succeed: %v", err)
	}
	_, errUsedUp := cp.RedeemInvite(usedInv.Code, nowNs)
	if errUsedUp == nil {
		t.Fatal("expected error on used-up code")
	}

	var svcErrU, svcErrE, svcErrUp *ServiceError
	if !errors.As(errUnknown, &svcErrU) || !errors.As(errExpired, &svcErrE) || !errors.As(errUsedUp, &svcErrUp) {
		t.Fatalf("all three errors must be *ServiceError: %v, %v, %v", errUnknown, errExpired, errUsedUp)
	}
	const expectedMsg = "invite_code: the code is not valid"
	if svcErrU.Code != "INVALID_ARGUMENT" || svcErrU.Message != expectedMsg {
		t.Fatalf("unknown code error: code=%s, msg=%s", svcErrU.Code, svcErrU.Message)
	}
	if svcErrE.Message != svcErrU.Message || svcErrUp.Message != svcErrU.Message {
		t.Fatalf("error messages must be identical for code enumeration protection:\nunknown: %s\nexpired: %s\nusedUp: %s",
			svcErrU.Message, svcErrE.Message, svcErrUp.Message)
	}

	// 4. RegisterTenantUser with mode invite creates user AND subscription, burning exactly one use
	regInv, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	regUser, err := cp.RegisterTenantUser(config.RegistrationInvite, regInv.Code, "reg_alice", "password-123456", nowNs)
	if err != nil {
		t.Fatalf("RegisterTenantUser with invite: %v", err)
	}
	if regUser.Username != "reg_alice" {
		t.Fatalf("expected username reg_alice, got %s", regUser.Username)
	}
	regSub, err := cp.Engine.GetActiveUserSubscription(regUser.ID)
	if err != nil {
		t.Fatalf("GetActiveUserSubscription: %v", err)
	}
	if regSub.PlanID != plan.ID {
		t.Fatalf("expected subscription plan %s, got %s", plan.ID, regSub.PlanID)
	}
	dbInv, err := cp.Engine.GetInvite(regInv.ID)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if dbInv.UsedCount != 1 {
		t.Fatalf("expected invite used_count 1, got %d", dbInv.UsedCount)
	}

	// 5. Atomicity check: RedeemInviteAndInsertUser must not burn a use when the insert fails (duplicate username)
	atomicInv, err := cp.CreateInvite(InviteInput{PlanID: plan.ID, MaxUses: 1}, "admin", nowNs)
	if err != nil {
		t.Fatalf("CreateInvite atomic: %v", err)
	}
	// Try registering with existing username "reg_alice"
	_, err = cp.RegisterTenantUser(config.RegistrationInvite, atomicInv.Code, "reg_alice", "password-123456", nowNs)
	if err == nil {
		t.Fatal("expected conflict registering duplicate username, got nil")
	}
	var svcErrConflict *ServiceError
	if !errors.As(err, &svcErrConflict) || svcErrConflict.Code != "CONFLICT" {
		t.Fatalf("expected CONFLICT error, got %v", err)
	}

	// Assert the invite's used_count was NOT burned and remains 0
	dbAtomicInv, err := cp.Engine.GetInvite(atomicInv.ID)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if dbAtomicInv.UsedCount != 0 {
		t.Fatalf("expected invite used_count 0 after registration conflict, got %d", dbAtomicInv.UsedCount)
	}

	// Now register a new user "reg_bob" using the same invite - must succeed
	regBob, err := cp.RegisterTenantUser(config.RegistrationInvite, atomicInv.Code, "reg_bob", "password-123456", nowNs)
	if err != nil {
		t.Fatalf("RegisterTenantUser(reg_bob) failed: %v", err)
	}
	if regBob.Username != "reg_bob" {
		t.Fatalf("expected username reg_bob, got %s", regBob.Username)
	}
	dbAtomicInvAfter, err := cp.Engine.GetInvite(atomicInv.ID)
	if err != nil {
		t.Fatalf("GetInvite: %v", err)
	}
	if dbAtomicInvAfter.UsedCount != 1 {
		t.Fatalf("expected invite used_count 1 after successful registration, got %d", dbAtomicInvAfter.UsedCount)
	}

	// 6. RegisterTenantUser with registration mode off returns Code REGISTRATION_CLOSED
	_, err = cp.RegisterTenantUser(config.RegistrationOff, "", "reg_closed", "password-123456", nowNs)
	if err == nil {
		t.Fatal("expected error registering when mode is off")
	}
	var svcErrClosed *ServiceError
	if !errors.As(err, &svcErrClosed) || svcErrClosed.Code != "REGISTRATION_CLOSED" {
		t.Fatalf("expected REGISTRATION_CLOSED, got %v", err)
	}

	// 7. Open mode needs no code
	regOpen, err := cp.RegisterTenantUser(config.RegistrationOpen, "", "reg_open_user", "password-123456", nowNs)
	if err != nil {
		t.Fatalf("RegisterTenantUser open mode failed: %v", err)
	}
	if regOpen.Username != "reg_open_user" {
		t.Fatalf("expected username reg_open_user, got %s", regOpen.Username)
	}
}

func TestUsage_EmptyDatabase(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	// All 4 usage queries must return zero values without error on an empty database
	sum, err := cp.UsageSummary(nowNs)
	if err != nil {
		t.Fatalf("UsageSummary on empty DB failed: %v", err)
	}
	if sum.ActiveUsers != 0 || sum.TotalUsers != 0 || sum.TodayBytes != 0 || sum.PeriodBytes != 0 {
		t.Fatalf("UsageSummary on empty DB should be zeroed: %+v", sum)
	}

	daily, err := cp.UsageDaily(7, nowNs)
	if err != nil {
		t.Fatalf("UsageDaily on empty DB failed: %v", err)
	}
	if len(daily) != 7 {
		t.Fatalf("expected 7 empty day buckets, got %d", len(daily))
	}
	for i, d := range daily {
		if d.TotalBytes != 0 || d.IngressBytes != 0 || d.EgressBytes != 0 {
			t.Fatalf("UsageDaily bucket %d on empty DB expected 0 bytes, got %+v", i, d)
		}
	}

	byUser, err := cp.UsageByUser(0, 0, 10, nowNs)
	if err != nil {
		t.Fatalf("UsageByUser on empty DB failed: %v", err)
	}
	if len(byUser) != 0 {
		t.Fatalf("expected empty UsageByUser on empty DB, got %d rows", len(byUser))
	}

	byPlat, err := cp.UsageByPlatform(7, nowNs)
	if err != nil {
		t.Fatalf("UsageByPlatform on empty DB failed: %v", err)
	}
	if len(byPlat) != 0 {
		t.Fatalf("expected empty UsageByPlatform on empty DB, got %d rows", len(byPlat))
	}
}

func TestUsage_SummaryAndAggregation(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	// Seed platform
	if err := cp.Engine.UpsertPlatform(model.Platform{
		ID:                     "plt_usage",
		Name:                   "usage_platform",
		ReverseProxyMissAction: "TREAT_AS_EMPTY",
		AllocationPolicy:       "BALANCED",
	}); err != nil {
		t.Fatalf("UpsertPlatform: %v", err)
	}

	// Create user, plan, and active subscription
	plan, err := cp.CreatePlan(PlanInput{
		Name:         "Usage Plan",
		TrafficBytes: 100000,
		DurationDays: 30,
		Status:       "active",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	user, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "usage_user",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	// Add subscription period used bytes
	subID := user.Subscription.ID
	if err := cp.Engine.AddSubscriptionUsedBytes(subID, 5000); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	// Seed daily usage rows
	const nsPerDay = int64(24 * time.Hour)
	todayStart := (nowNs / nsPerDay) * nsPerDay
	row := model.UsageRow{
		KeyID:        "k_usage",
		UserID:       user.ID,
		PlatformID:   "plt_usage",
		TSNs:         todayStart + 1000,
		IngressBytes: 3000,
		EgressBytes:  4000,
		Requests:     50,
		Errors:       0,
	}
	if err := cp.Engine.UpsertUsageDaily([]model.UsageRow{row}); err != nil {
		t.Fatalf("UpsertUsageDaily: %v", err)
	}

	// 1. UsageSummary
	sum, err := cp.UsageSummary(nowNs)
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if sum.ActiveUsers != 1 {
		t.Fatalf("expected ActiveUsers 1, got %d", sum.ActiveUsers)
	}
	if sum.TotalUsers != 1 {
		t.Fatalf("expected TotalUsers 1, got %d", sum.TotalUsers)
	}
	if sum.TodayBytes != 7000 {
		t.Fatalf("expected TodayBytes 7000, got %d", sum.TodayBytes)
	}
	if sum.PeriodBytes != 5000 {
		t.Fatalf("expected PeriodBytes 5000, got %d", sum.PeriodBytes)
	}

	// 2. UsageDaily
	daily, err := cp.UsageDaily(7, nowNs)
	if err != nil {
		t.Fatalf("UsageDaily: %v", err)
	}
	if len(daily) != 7 {
		t.Fatalf("expected 7 days, got %d", len(daily))
	}
	var totalDailyBytes int64
	for _, d := range daily {
		totalDailyBytes += d.TotalBytes
	}
	if totalDailyBytes != 7000 {
		t.Fatalf("expected total daily bytes 7000, got %d", totalDailyBytes)
	}

	// 3. UsageByUser
	byUser, err := cp.UsageByUser(0, 0, 10, nowNs)
	if err != nil {
		t.Fatalf("UsageByUser: %v", err)
	}
	if len(byUser) != 1 {
		t.Fatalf("expected 1 user row, got %d", len(byUser))
	}
	if byUser[0].UserID != user.ID || byUser[0].Username != "usage_user" ||
		byUser[0].TotalBytes != 7000 || byUser[0].PeriodUsedBytes != 5000 {
		t.Fatalf("UsageByUser row mismatch: %+v", byUser[0])
	}

	// 4. UsageByPlatform
	byPlat, err := cp.UsageByPlatform(7, nowNs)
	if err != nil {
		t.Fatalf("UsageByPlatform: %v", err)
	}
	if len(byPlat) != 1 {
		t.Fatalf("expected 1 platform row, got %d", len(byPlat))
	}
	if byPlat[0].PlatformID != "plt_usage" || byPlat[0].PlatformName != "usage_platform" || byPlat[0].TotalBytes != 7000 {
		t.Fatalf("UsageByPlatform row mismatch: %+v", byPlat[0])
	}
}

func TestSelfService_OverviewAndSessions(t *testing.T) {
	cp, _, _, nowNs := newTenantTestService(t)

	// 1. SelfSubscription for user with none returns nil view and no error
	uNoSub, err := cp.CreateAccount("user_nosub", "password-123456", "user", nowNs)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	nilSubView, err := cp.SelfSubscription(uNoSub.ID)
	if err != nil {
		t.Fatalf("SelfSubscription on user without subscription: %v", err)
	}
	if nilSubView != nil {
		t.Fatalf("expected nil subscription view, got %+v", nilSubView)
	}

	// 2. SelfOverview for a user with subscription reports period usage and remaining bytes
	plan, err := cp.CreatePlan(PlanInput{
		Name:         "Self Plan",
		TrafficBytes: 100000,
		DurationDays: 30,
		Status:       "active",
	}, nowNs)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}

	uSub, err := cp.CreateTenantUser(TenantUserInput{
		Username:     "user_with_sub",
		Password:     "password-123456",
		Role:         "user",
		PlanID:       plan.ID,
		DurationDays: 30,
	}, nowNs)
	if err != nil {
		t.Fatalf("CreateTenantUser: %v", err)
	}

	// Add period used bytes
	if err := cp.Engine.AddSubscriptionUsedBytes(uSub.Subscription.ID, 35000); err != nil {
		t.Fatalf("AddSubscriptionUsedBytes: %v", err)
	}

	overview, err := cp.SelfOverview(uSub.ID, nowNs)
	if err != nil {
		t.Fatalf("SelfOverview: %v", err)
	}
	if overview.Subscription == nil {
		t.Fatal("expected non-nil subscription in SelfOverview")
	}
	if overview.PeriodUsedBytes != 35000 {
		t.Fatalf("expected period_used_bytes 35000, got %d", overview.PeriodUsedBytes)
	}
	if overview.TrafficBytes != 100000 {
		t.Fatalf("expected traffic_bytes 100000, got %d", overview.TrafficBytes)
	}
	if overview.RemainingBytes == nil || *overview.RemainingBytes != 65000 {
		var got int64
		if overview.RemainingBytes != nil {
			got = *overview.RemainingBytes
		}
		t.Fatalf("expected remaining_bytes 65000, got %d", got)
	}

	// 3. ListUserSessions marks current session (Current == true) when given raw session id,
	// and marks false for an id belonging to another user
	rawSess1 := "raw_session_user_1"
	rawSess2 := "raw_session_user_2"
	rawSessOther := "raw_session_other_user"

	if err := cp.Engine.InsertSession(model.Session{
		IDHash:      auth.HashSessionID(rawSess1),
		UserID:      uSub.ID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + int64(12*time.Hour),
		IP:          "192.0.2.1",
		UA:          "Agent1",
	}); err != nil {
		t.Fatalf("InsertSession sess1: %v", err)
	}
	if err := cp.Engine.InsertSession(model.Session{
		IDHash:      auth.HashSessionID(rawSess2),
		UserID:      uSub.ID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + int64(12*time.Hour),
		IP:          "192.0.2.2",
		UA:          "Agent2",
	}); err != nil {
		t.Fatalf("InsertSession sess2: %v", err)
	}
	if err := cp.Engine.InsertSession(model.Session{
		IDHash:      auth.HashSessionID(rawSessOther),
		UserID:      uNoSub.ID,
		CreatedAtNs: nowNs,
		ExpiresAtNs: nowNs + int64(12*time.Hour),
		IP:          "192.0.2.3",
		UA:          "AgentOther",
	}); err != nil {
		t.Fatalf("InsertSession sessOther: %v", err)
	}

	// List sessions passing rawSess1 as current
	sessions, err := cp.ListUserSessions(uSub.ID, rawSess1, nowNs)
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions for uSub, got %d", len(sessions))
	}
	currentCount := 0
	for _, s := range sessions {
		if s.ID == auth.HashSessionID(rawSess1) {
			if !s.Current {
				t.Fatalf("expected session %s to have Current=true", s.ID)
			}
			currentCount++
		} else {
			if s.Current {
				t.Fatalf("expected session %s to have Current=false", s.ID)
			}
		}
	}
	if currentCount != 1 {
		t.Fatalf("expected exactly 1 current session, got %d", currentCount)
	}

	// List sessions passing rawSessOther (from different user)
	sessionsOther, err := cp.ListUserSessions(uSub.ID, rawSessOther, nowNs)
	if err != nil {
		t.Fatalf("ListUserSessions with other user's session: %v", err)
	}
	for _, s := range sessionsOther {
		if s.Current {
			t.Fatalf("expected Current=false for all uSub sessions when given other user's id, got %+v", s)
		}
	}

	// 4. RevokeUserSession refuses session id owned by a different user
	err = cp.RevokeUserSession(uSub.ID, auth.HashSessionID(rawSessOther))
	if err == nil {
		t.Fatal("expected error revoking session of another user")
	}
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) || svcErr.Code != "NOT_FOUND" {
		t.Fatalf("expected NOT_FOUND revoking other user's session, got %v", err)
	}

	// Revoke user's own session
	if err := cp.RevokeUserSession(uSub.ID, auth.HashSessionID(rawSess1)); err != nil {
		t.Fatalf("RevokeUserSession own session failed: %v", err)
	}
	sessionsAfterRevoke, err := cp.ListUserSessions(uSub.ID, "", nowNs)
	if err != nil {
		t.Fatalf("ListUserSessions: %v", err)
	}
	if len(sessionsAfterRevoke) != 1 || sessionsAfterRevoke[0].ID != auth.HashSessionID(rawSess2) {
		t.Fatalf("expected only session 2 remaining after revocation, got %+v", sessionsAfterRevoke)
	}
}
