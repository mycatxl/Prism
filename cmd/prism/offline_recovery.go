package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"prism/internal/auth"
	"prism/internal/model"
	"prism/internal/state"
)

// Offline commands use explicit paths, refuse a live service, and never create a
// replacement database/pepper when pointed at the wrong directory.
func openRecoveryStore(stateDir, cacheDir string) (*state.StateEngine, func(), error) {
	for _, p := range []string{filepath.Join(stateDir, "state.db"), filepath.Join(stateDir, auth.PepperFileName), filepath.Join(cacheDir, "cache.db")} {
		st, err := os.Lstat(p)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return nil, nil, fmt.Errorf("offline recovery requires existing private database and pepper files (0600)")
		}
	}
	lock, err := state.AcquireDirLock(stateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("stop Prism before offline recovery: %w", err)
	}
	engine, closer, err := state.PersistenceBootstrap(stateDir, cacheDir)
	if err != nil {
		_ = lock.Release()
		return nil, nil, err
	}
	return engine, func() { closer.Close(); _ = lock.Release() }, nil
}

func runRecoverAdminCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("recover-admin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "existing private state directory")
	cacheDir := fs.String("cache-dir", "", "existing private cache directory")
	username := fs.String("username", "", "unused recovery administrator name")
	reason := fs.String("reason", "", "incident reference, without secrets")
	confirm := fs.Bool("confirm-offline", false, "confirm local owner recovery after stopping Prism")
	passwordStdin := fs.Bool("password-stdin", false, "read new password from standard input; never print it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !*confirm || !*passwordStdin || *stateDir == "" || *cacheDir == "" || len(*username) < 3 || len(*username) > 64 || strings.TrimSpace(*reason) == "" || len(*reason) > 512 {
		return errors.New("recover-admin requires --state-dir, --cache-dir, unused --username, --reason, --password-stdin and --confirm-offline")
	}
	engine, close, err := openRecoveryStore(*stateDir, *cacheDir)
	if err != nil {
		return err
	}
	defer close()
	raw, err := io.ReadAll(io.LimitReader(stdin, 1026))
	if err != nil {
		return errors.New("could not read recovery password")
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("recovery password must be 12–1024 bytes")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return errors.New("could not hash recovery password")
	}
	now := time.Now().UnixNano()
	err = engine.RecoverAdministrator(model.User{ID: uuid.NewString(), Username: *username, PasswordHash: hash, Role: model.UserRoleAdmin, Status: model.UserStatusActive, CreatedAtNs: now, UpdatedAtNs: now}, *reason)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Recovery administrator created and audited. Existing factors and replay watermarks preserved. Sign in locally, enroll a fresh TOTP factor, and disable the lost administrator.")
	return nil
}

func runUsageRecoveryCommand(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("usage-recovery", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "existing private state directory")
	cacheDir := fs.String("cache-dir", "", "existing private cache directory")
	id := fs.String("reservation", "", "reservation to reconcile (omit to list)")
	actual := fs.Int64("actual-bytes", -1, "independently verified bytes delivered; never guess")
	reason := fs.String("reason", "", "incident/evidence reference without secrets")
	confirm := fs.Bool("confirm-offline", false, "confirm independently verified byte outcome")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *stateDir == "" || *cacheDir == "" {
		return errors.New("usage-recovery requires --state-dir and --cache-dir")
	}
	engine, close, err := openRecoveryStore(*stateDir, *cacheDir)
	if err != nil {
		return err
	}
	defer close()
	if *id == "" {
		rows, err := engine.PendingUsage()
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(rows)
	}
	if !*confirm || *actual < 0 || strings.TrimSpace(*reason) == "" {
		return errors.New("reconciliation requires --actual-bytes, --reason and --confirm-offline; leave unknown outcomes closed")
	}
	if err := engine.ReconcileUsage(*id, *actual, *reason, time.Now().UnixNano()); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Usage liability reconciled and audited; no balance transaction was created.")
	return nil
}
