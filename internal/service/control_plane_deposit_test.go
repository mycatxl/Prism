package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"prism/internal/config"
	"prism/internal/model"
	"prism/internal/tron"
)

// Deposits are off unless an operator configured a receiving address, so every
// test here starts by saying where the money should arrive.
const testUSDTAddress = "TQn9Y2khEsLJW1ChVWFMSMeRDow5KcbLSE"

func newDepositTestService(t *testing.T) (*ControlPlaneService, int64) {
	t.Helper()
	cp, _, _, nowNs := newTenantTestService(t)
	cp.EnvCfg = &config.EnvConfig{
		USDTAddress:  testUSDTAddress,
		USDTContract: config.DefaultUSDTContract,
	}
	return cp, nowNs
}

func createDepositUser(t *testing.T, cp *ControlPlaneService, username string) string {
	t.Helper()
	account, err := cp.CreateAccount(username, username+"-password-1234", model.UserRoleUser, 1)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return account.ID
}

func TestDepositInfoReportsConfiguredState(t *testing.T) {
	cp, _ := newDepositTestService(t)

	info := cp.SelfDepositInfo()
	if !info.Enabled {
		t.Fatal("deposits must be enabled when an address is configured")
	}
	if info.Address != testUSDTAddress {
		t.Fatalf("address: got %q, want %q", info.Address, testUSDTAddress)
	}
	if info.MinMicro != depositMinMicro {
		t.Fatalf("min: got %d, want %d", info.MinMicro, depositMinMicro)
	}
	if info.WindowSecs != 900 {
		t.Fatalf("window: got %d seconds, want 900", info.WindowSecs)
	}

	// An unconfigured address turns the feature off rather than breaking it.
	cp.EnvCfg.USDTAddress = ""
	if cp.SelfDepositInfo().Enabled {
		t.Fatal("deposits must be disabled when no address is configured")
	}
}

func TestDepositCandidatesSkipWholeAmounts(t *testing.T) {
	// Asking for a whole amount must still produce a fractional one: a round
	// number is the most likely to collide with an unrelated transfer.
	candidates := depositCandidates(10_000_000)
	if len(candidates) == 0 {
		t.Fatal("candidates must not be empty")
	}
	if candidates[0] != 10_000_001 {
		t.Fatalf("first candidate: got %d, want 10000001", candidates[0])
	}
	for _, amount := range candidates {
		if amount%1_000_000 == 0 {
			t.Fatalf("candidate %d is a whole amount", amount)
		}
	}
	if len(candidates) > depositAmountAttempts {
		t.Fatalf("candidates: got %d, want at most %d", len(candidates), depositAmountAttempts)
	}
}

func TestSelfDepositRejectsOutOfRangeAmounts(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "deposit-range")

	if _, err := cp.SelfDeposit(userID, depositMinMicro-1, nowNs); err == nil {
		t.Fatal("an amount below the minimum must be refused")
	}
	if _, err := cp.SelfDeposit(userID, depositMaxMicro+1, nowNs); err == nil {
		t.Fatal("an amount above the maximum must be refused")
	}
}

// Two intents asking for the same amount get different amounts to pay, and both
// are inside the requested range.
func TestSelfDepositHandsOutDistinctAmounts(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	first := createDepositUser(t, cp, "deposit-one")
	second := createDepositUser(t, cp, "deposit-two")

	a, err := cp.SelfDeposit(first, 10_000_000, nowNs)
	if err != nil {
		t.Fatalf("first deposit: %v", err)
	}
	b, err := cp.SelfDeposit(second, 10_000_000, nowNs)
	if err != nil {
		t.Fatalf("second deposit: %v", err)
	}
	if a.AmountMicro == b.AmountMicro {
		t.Fatalf("two intents share the amount %d", a.AmountMicro)
	}
	if a.AmountMicro != 10_000_001 || b.AmountMicro != 10_000_002 {
		t.Fatalf("amounts: got %d and %d, want 10000001 and 10000002", a.AmountMicro, b.AmountMicro)
	}
	if a.Status != model.DepositStatusPending {
		t.Fatalf("status: got %q, want pending", a.Status)
	}
	if a.ExpiresAtNs-a.CreatedAtNs != depositWindowNs {
		t.Fatalf("payment window: got %d, want %d", a.ExpiresAtNs-a.CreatedAtNs, depositWindowNs)
	}
	if a.HoldUntilNs-a.ExpiresAtNs != depositHoldNs {
		t.Fatalf("silent hold: got %d, want %d", a.HoldUntilNs-a.ExpiresAtNs, depositHoldNs)
	}
}

// Another user's intent must be indistinguishable from one that does not exist.
func TestSelfDepositIntentHidesOtherUsersIntents(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	owner := createDepositUser(t, cp, "deposit-owner")
	other := createDepositUser(t, cp, "deposit-other")

	intent, err := cp.SelfDeposit(owner, 5_000_000, nowNs)
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}

	if _, err := cp.SelfDepositIntent(owner, intent.ID); err != nil {
		t.Fatalf("owner reading own intent: %v", err)
	}

	_, err = cp.SelfDepositIntent(other, intent.ID)
	var svcErr *ServiceError
	if !errors.As(err, &svcErr) {
		t.Fatalf("other user: got %v, want a service error", err)
	}
	if svcErr.Code != "NOT_FOUND" {
		t.Fatalf("other user: got code %q, want NOT_FOUND", svcErr.Code)
	}

	// A made-up id answers exactly the same way.
	_, err = cp.SelfDepositIntent(other, "dep_does_not_exist")
	if !errors.As(err, &svcErr) || svcErr.Code != "NOT_FOUND" {
		t.Fatalf("unknown id: got %v, want NOT_FOUND", err)
	}
}

func TestSelfDepositRequiresConfiguredAddress(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "deposit-off")
	cp.EnvCfg.USDTAddress = ""

	if _, err := cp.SelfDeposit(userID, 10_000_000, nowNs); err == nil {
		t.Fatal("a deposit must be refused when no address is configured")
	}
}

// The state layer is reached through the engine, so this checks the wiring the
// service adds: the intent is stored for the caller and listed back.
func TestSelfDepositsListsOwnIntents(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "deposit-list")

	if _, err := cp.SelfDeposit(userID, 2_000_000, nowNs); err != nil {
		t.Fatalf("deposit: %v", err)
	}
	got, err := cp.SelfDeposits(userID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("intents: got %d, want 1", len(got))
	}
	if got[0].Address != testUSDTAddress {
		t.Fatalf("address: got %q, want %q", got[0].Address, testUSDTAddress)
	}
}

// fakeChain is a TronGrid stand-in: it answers the TRC20 endpoint with the
// transfers it was given, and records the min_timestamp each query carried so a
// test can see the cursor move.
type fakeChain struct {
	transfers []fakeTransfer
	queries   []string
	failWith  int
}

type fakeTransfer struct {
	txID     string
	amount   int64
	blockMs  int64
	to       string
	contract string
}

func (f *fakeChain) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.queries = append(f.queries, r.URL.Query().Get("min_timestamp"))
		if f.failWith != 0 {
			w.WriteHeader(f.failWith)
			_, _ = w.Write([]byte(`{"error":"upstream down"}`))
			return
		}
		if got := r.URL.Query().Get("only_confirmed"); got != "true" {
			t.Errorf("only_confirmed: got %q, want true", got)
		}
		type row struct {
			TransactionID string `json:"transaction_id"`
			BlockTS       int64  `json:"block_timestamp"`
			From          string `json:"from"`
			To            string `json:"to"`
			Type          string `json:"type"`
			Value         string `json:"value"`
			TokenInfo     struct {
				Address  string `json:"address"`
				Decimals int    `json:"decimals"`
			} `json:"token_info"`
		}
		payload := struct {
			Data []row `json:"data"`
		}{}
		for _, tr := range f.transfers {
			var out row
			out.TransactionID = tr.txID
			out.BlockTS = tr.blockMs
			out.From = "TFromAddress"
			out.To = tr.to
			out.Type = "Transfer"
			out.Value = fmt.Sprintf("%d", tr.amount)
			out.TokenInfo.Address = tr.contract
			out.TokenInfo.Decimals = 6
			payload.Data = append(payload.Data, out)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}
}

// A transfer that matches an intent's amount and lands inside its window is
// credited, and the intent records the transaction.
func TestScanDepositsCreditsAMatchingTransfer(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "scan-credit")
	intent, err := cp.SelfDeposit(userID, 10_000_000, nowNs)
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}

	chain := &fakeChain{transfers: []fakeTransfer{{
		txID:     "tx-match",
		amount:   intent.AmountMicro,
		blockMs:  (intent.CreatedAtNs + 1_000_000_000) / 1_000_000,
		to:       testUSDTAddress,
		contract: config.DefaultUSDTContract,
	}}}
	srv := httptest.NewServer(chain.handler(t))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	credited, err := cp.ScanDeposits(context.Background(), nowNs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if credited != 1 {
		t.Fatalf("credited: got %d, want 1", credited)
	}

	got, err := cp.SelfDepositIntent(userID, intent.ID)
	if err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if got.Status != model.DepositStatusPaid {
		t.Fatalf("status: got %q, want paid", got.Status)
	}
	if got.TxID != "tx-match" {
		t.Fatalf("tx id: got %q, want tx-match", got.TxID)
	}
	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != intent.AmountMicro {
		t.Fatalf("balance: got %d, want %d", balance, intent.AmountMicro)
	}
}

// A second scan of the same transfer must not credit twice.
func TestScanDepositsIsIdempotentAcrossScans(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	userID := createDepositUser(t, cp, "scan-replay")
	intent, err := cp.SelfDeposit(userID, 10_000_000, nowNs)
	if err != nil {
		t.Fatalf("deposit: %v", err)
	}

	chain := &fakeChain{transfers: []fakeTransfer{{
		txID:     "tx-once",
		amount:   intent.AmountMicro,
		blockMs:  (intent.CreatedAtNs + 1_000_000_000) / 1_000_000,
		to:       testUSDTAddress,
		contract: config.DefaultUSDTContract,
	}}}
	srv := httptest.NewServer(chain.handler(t))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	if _, err := cp.ScanDeposits(context.Background(), nowNs); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	again, err := cp.ScanDeposits(context.Background(), nowNs)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if again != 0 {
		t.Fatalf("second scan credited %d, want 0", again)
	}
	balance, err := cp.Engine.GetBalance(userID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if balance != intent.AmountMicro {
		t.Fatalf("balance after replay: got %d, want %d", balance, intent.AmountMicro)
	}
}

// A transfer nobody asked for is skipped, and the cursor still moves past it so
// the next poll does not read it again.
func TestScanDepositsSkipsUnknownAmountsAndAdvancesCursor(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	if _, err := cp.SelfDeposit(createDepositUser(t, cp, "scan-cursor"), 10_000_000, nowNs); err != nil {
		t.Fatalf("deposit: %v", err)
	}

	blockMs := nowNs/1_000_000 + 1_000
	chain := &fakeChain{transfers: []fakeTransfer{{
		txID:     "tx-stranger",
		amount:   77_000_077,
		blockMs:  blockMs,
		to:       testUSDTAddress,
		contract: config.DefaultUSDTContract,
	}}}
	srv := httptest.NewServer(chain.handler(t))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	credited, err := cp.ScanDeposits(context.Background(), nowNs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if credited != 0 {
		t.Fatalf("credited: got %d, want 0", credited)
	}
	cursor, err := cp.Engine.ChainCursor(depositCursorName)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != blockMs {
		t.Fatalf("cursor: got %d, want %d", cursor, blockMs)
	}
	// A zero watermark is omitted from the query, so the first poll reads the
	// whole confirmed history of the address.
	if len(chain.queries) != 1 || chain.queries[0] != "" {
		t.Fatalf("first query min_timestamp: got %v, want one query without a watermark", chain.queries)
	}

	// The next scan must ask from the new watermark.
	chain.transfers = nil
	if _, err := cp.ScanDeposits(context.Background(), nowNs); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(chain.queries) != 2 {
		t.Fatalf("queries: got %d, want 2", len(chain.queries))
	}
	if chain.queries[1] != fmt.Sprintf("%d", blockMs) {
		t.Fatalf("second query min_timestamp: got %q, want %d", chain.queries[1], blockMs)
	}
}

// A page holding only transfers this wallet sent still has to move the cursor.
//
// TronGrid reports both directions for the account, so an operator who pays out
// of the deposit wallet fills its page with outgoing rows. Advancing the
// watermark only past rows addressed to us would freeze the scan on that page
// and every later deposit would stay invisible for ever.
func TestScanDepositsAdvancesCursorPastForeignTransfers(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	blockMs := nowNs/1_000_000 + 1_000
	chain := &fakeChain{transfers: []fakeTransfer{{
		txID:     "tx-outgoing",
		amount:   5_000_000,
		blockMs:  blockMs,
		to:       "TSomeoneElseAltogether",
		contract: config.DefaultUSDTContract,
	}}}
	srv := httptest.NewServer(chain.handler(t))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	credited, err := cp.ScanDeposits(context.Background(), nowNs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if credited != 0 {
		t.Fatalf("credited: got %d, want 0", credited)
	}
	cursor, err := cp.Engine.ChainCursor(depositCursorName)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != blockMs {
		t.Fatalf("cursor: got %d, want %d (a page of foreign rows must still move it)", cursor, blockMs)
	}

	// With the cursor advanced, the next poll asks from that watermark instead
	// of re-reading the same page.
	chain.transfers = nil
	if _, err := cp.ScanDeposits(context.Background(), nowNs); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(chain.queries) != 2 || chain.queries[1] != fmt.Sprintf("%d", blockMs) {
		t.Fatalf("second query min_timestamp: got %v, want %d", chain.queries, blockMs)
	}
}

// Dust below the minimum is ignored rather than credited.
func TestScanDepositsIgnoresDust(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	if _, err := cp.SelfDeposit(createDepositUser(t, cp, "scan-dust"), 10_000_000, nowNs); err != nil {
		t.Fatalf("deposit: %v", err)
	}

	chain := &fakeChain{transfers: []fakeTransfer{{
		txID:     "tx-dust",
		amount:   depositMinMicro - 1,
		blockMs:  nowNs/1_000_000 + 1_000,
		to:       testUSDTAddress,
		contract: config.DefaultUSDTContract,
	}}}
	srv := httptest.NewServer(chain.handler(t))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	credited, err := cp.ScanDeposits(context.Background(), nowNs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if credited != 0 {
		t.Fatalf("credited dust: got %d, want 0", credited)
	}
}

// A chain that answers with an error must surface it, not swallow it: a silent
// poller looks exactly like "nobody paid".
func TestScanDepositsReportsChainFailures(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	chain := &fakeChain{failWith: http.StatusBadGateway}
	srv := httptest.NewServer(chain.handler(t))
	t.Cleanup(srv.Close)
	cp.Tron = tron.NewClient(srv.URL, "", cp.EnvCfg.USDTContract)

	if _, err := cp.ScanDeposits(context.Background(), nowNs); err == nil {
		t.Fatal("a failing chain must return an error")
	}
}

// With no address or no client there is nothing to watch, and the scan says so
// by crediting nothing rather than failing.
func TestScanDepositsNoOpsWhenUnconfigured(t *testing.T) {
	cp, nowNs := newDepositTestService(t)
	cp.EnvCfg.USDTAddress = ""
	if credited, err := cp.ScanDeposits(context.Background(), nowNs); err != nil || credited != 0 {
		t.Fatalf("no address: got (%d, %v), want (0, nil)", credited, err)
	}

	cp.EnvCfg.USDTAddress = testUSDTAddress
	cp.Tron = nil
	if credited, err := cp.ScanDeposits(context.Background(), nowNs); err != nil || credited != 0 {
		t.Fatalf("no client: got (%d, %v), want (0, nil)", credited, err)
	}
}
