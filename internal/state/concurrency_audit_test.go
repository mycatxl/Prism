package state

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"prism/internal/model"
)

func TestConcurrentTransactionsWaitForIndependentWriter(t *testing.T) {
	for _, operation := range []string{"balance", "chain", "ldc", "session", "totp", "usage"} {
		t.Run(operation, func(t *testing.T) {
			engine, dir, _ := newTestEngine(t)
			repo := engine.StateRepo
			mustInsertUser(t, repo, "u", "user")
			user, err := repo.GetUser("u")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.InsertDepositIntent(newDepositIntent("d", "u", 0, depositTestNowNs), []int64{100}, depositTestNowNs); err != nil {
				t.Fatal(err)
			}
			if err := repo.InsertLDCDeposit(model.LDCDeposit{OutTradeNo: "ldc", UserID: "u", AmountMicro: 100, Status: model.LDCDepositStatusPending}); err != nil {
				t.Fatal(err)
			}
			if err := repo.UpsertUserSubscription(model.UserSubscription{ID: "s", UserID: "u", PlanSnapshotJSON: "{}", PeriodStartNs: 1, Status: model.SubscriptionStatusActive}); err != nil {
				t.Fatal(err)
			}
			other, err := OpenDB(dir + "/state.db")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			// Force replacement connections to exercise DSN configuration too.
			repo.db.SetMaxIdleConns(0)
			tx, err := other.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.Exec("UPDATE users SET last_login_ns=2 WHERE id='u'"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				entry := model.BalanceEntry{ID: "entry", UserID: "u", DeltaMicro: 100, Reason: model.BalanceReasonAdminAdjust, CreatedAtNs: depositTestNowNs}
				var err error
				switch operation {
				case "balance":
					_, err = repo.ApplyBalanceDelta(entry)
				case "chain":
					_, err = repo.CreditDeposit("tx", 100, depositTestNowNs, depositTestNowNs, "cursor", 1, entry)
				case "ldc":
					err = repo.CreditLDCDeposit("ldc", "trade", entry)
				case "session":
					var ok bool
					ok, err = repo.InsertVerifiedSession(model.Session{IDHash: "session", UserID: "u"}, *user)
					if err == nil && !ok {
						err = fmt.Errorf("login refused")
					}
				case "totp":
					err = repo.UpdateVerifiedTOTP(*user, []byte("sealed"), 3)
				case "usage":
					_, err = repo.FlushUsageHourlyAndCharge(nil, []model.UsageCharge{{UserID: "u", SubscriptionID: "s", PeriodStartNs: 1, Bytes: 7}})
				}
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("operation finished while writer held WAL: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("transaction did not resume")
			}
		})
	}
}

func TestConcurrentBalanceBurstAcrossIndependentHandles(t *testing.T) {
	engine, dir, _ := newTestEngine(t)
	mustInsertUser(t, engine.StateRepo, "u", "user")
	const workers, credits = 16, 16
	repos := make([]*StateRepo, workers)
	for i := range repos {
		db, err := OpenDB(dir + "/state.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		repos[i] = newStateRepo(db)
	}
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for n := range credits {
				_, err := repo.ApplyBalanceDelta(model.BalanceEntry{ID: fmt.Sprintf("%d-%d", i, n), UserID: "u", DeltaMicro: 1, Reason: model.BalanceReasonAdminAdjust})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	balance, err := engine.GetBalance("u")
	if err != nil || balance != workers*credits {
		t.Fatalf("balance=%d error=%v", balance, err)
	}
	ledger, err := engine.ListBalanceLedger("u", 0)
	if err != nil || len(ledger) != workers*credits {
		t.Fatalf("ledger=%d error=%v", len(ledger), err)
	}
}

func TestConcurrentCacheFlushCannotResurrectDeletedNode(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	captured, release := make(chan struct{}), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	engine.MarkNodeStatic("n")
	go func() {
		first <- engine.FlushDirtySets(CacheReaders{ReadNodeStatic: func(string) *model.NodeStatic {
			close(captured)
			<-release
			return &model.NodeStatic{Hash: "n", RawOptions: []byte(`{}`)}
		}})
	}()
	<-captured
	engine.MarkNodeStaticDelete("n")
	go func() { second <- engine.FlushDirtySets(CacheReaders{}) }()
	select {
	case err := <-second:
		close(release)
		<-first
		t.Fatalf("newer flush overtook captured snapshot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	nodes, err := engine.LoadAllNodesStatic()
	if err != nil || len(nodes) != 0 || engine.DirtyCount() != 0 {
		t.Fatalf("nodes=%v dirty=%d err=%v", nodes, engine.DirtyCount(), err)
	}
}

func TestSubscriptionStaleTermsCannotUndoRevocationOrStop(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			repo, original := purchaseStateFixture(t, false)
			if stop {
				stopped := original
				stopped.PlanSnapshotJSON = `{"status":"disabled"}`
				if err := repo.UpdateUserSubscriptionTerms(stopped, original); err != nil {
					t.Fatal(err)
				}
			} else if err := repo.RevokeUserSubscription(original.ID); err != nil {
				t.Fatal(err)
			}
			stale := original
			stale.ExpiresAtNs++
			if err := repo.UpdateUserSubscriptionTerms(stale, original); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale edit = %v", err)
			}
		})
	}
}

func TestPeriodResetSkipsReplacedSubscription(t *testing.T) {
	repo, old := purchaseStateFixture(t, false)
	if err := repo.RevokeUserSubscription(old.ID); err != nil {
		t.Fatal(err)
	}
	changed, err := repo.ResetSubscriptionPeriodIfCurrent(old.ID, old.PeriodStartNs, 100)
	if err != nil || changed {
		t.Fatalf("revoked period reset: changed=%v err=%v", changed, err)
	}
}
