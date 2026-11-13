package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"prism/internal/auth"
	"prism/internal/model"
	"prism/internal/service"
	"prism/internal/state"
)

func TestOfflineRecoveryPreservesFutureReplayWatermark(t *testing.T) {
	dir := t.TempDir()
	initEnv(t, dir)
	if err := runInitCommand([]string{"--dir", dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	sd, cd := filepath.Join(dir, "state"), filepath.Join(dir, "cache")
	engine, closer, err := state.PersistenceBootstrap(sd, cd)
	if err != nil {
		t.Fatal(err)
	}
	pepper, err := auth.LoadPepper(sd)
	if err != nil {
		t.Fatal(err)
	}
	password := "fixture-password-kept-private"
	cp := &service.ControlPlaneService{Engine: engine, Pepper: pepper}
	base := time.Now().UTC().Truncate(30 * time.Second)
	user, err := cp.CreateAccount("lost-admin", password, model.UserRoleAdmin, base.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	enrol, err := cp.StartTOTPEnrolment(user.ID, "recovery-test")
	if err != nil {
		t.Fatal(err)
	}
	code, err := auth.TOTPCode(enrol.Secret, base)
	if err != nil {
		t.Fatal(err)
	}
	if err = cp.ConfirmTOTPEnrolment(user.ID, enrol.Secret, code, base.UnixNano()); err != nil {
		t.Fatal(err)
	}
	clock := base.Add(time.Hour)
	sessions := &auth.Service{Store: engine, Pepper: pepper, Now: func() int64 { return clock.UnixNano() }}
	future, err := auth.TOTPCode(enrol.Secret, clock)
	if err != nil {
		t.Fatal(err)
	}
	// Use real authentication rather than writing a replay watermark by hand.
	if _, err = sessions.Login("lost-admin", password, future, "fixture-client", "127.0.0.1", "fixture"); err != nil {
		t.Fatal(err)
	}
	clock = base.Add(time.Minute)
	current, _ := auth.TOTPCode(enrol.Secret, clock)
	if _, err = sessions.Login("lost-admin", password, current, "fixture-client", "127.0.0.1", "fixture"); !errors.Is(err, auth.ErrTOTPAlreadyUsed) {
		t.Fatal("future watermark was not enforced")
	}
	before, err := engine.GetUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()
	args := []string{"--state-dir", sd, "--cache-dir", cd, "--username", "recovery", "--reason", "INC-test", "--confirm-offline", "--password-stdin"}
	lock, err := state.AcquireDirLock(sd)
	if err != nil {
		t.Fatal(err)
	}
	if err = runRecoverAdminCommand(args, strings.NewReader(password), io.Discard, io.Discard); err == nil {
		t.Fatal("recovery ran against live directory lock")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = runRecoverAdminCommand(args, strings.NewReader(password), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), password) {
		t.Fatal("credential in recovery output")
	}
	engine, closer, err = state.PersistenceBootstrap(sd, cd)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	after, err := engine.GetUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TOTPLastUsedNs != before.TOTPLastUsedNs || !bytes.Equal(after.TOTPSecretEnc, before.TOTPSecretEnc) || after.PasswordHash != before.PasswordHash {
		t.Fatal("old credentials or replay watermark changed")
	}
	sessions.Store = engine
	if _, err = sessions.Login("recovery", password, "", "fixture-client", "127.0.0.1", "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Login("lost-admin", password, current, "fixture-client", "127.0.0.1", "fixture"); !errors.Is(err, auth.ErrTOTPAlreadyUsed) {
		t.Fatal("recovery bypassed replay protection")
	}
	audit, err := engine.ListAudit(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range audit {
		if row.Action == "offline.recover_admin" {
			found = true
		}
	}
	if !found {
		t.Fatal("offline recovery not audited")
	}
}
