// Package tron reads confirmed TRC20 and native TRX transfers. It never signs
// transactions and has no wallet private key.
package tron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Transfer struct {
	TxID        string
	From        string
	To          string
	AmountMicro int64
	BlockTSMs   int64
}

type TransferPage struct {
	Transfers       []Transfer
	NewestRawMs     int64
	NextFingerprint string
}

// ErrInvalidFingerprint means the provider explicitly rejected a continuation.
// Other errors must not discard a scan's saved pagination position.
var ErrInvalidFingerprint = errors.New("tron: invalid or expired pagination fingerprint")

type Client struct {
	baseURL        string
	apiKey         string
	contract       string
	http           *http.Client
	fingerprint    string
	maxTimestampMs int64
}

func NewClient(baseURL, apiKey, contract string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, contract: contract,
		http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
}

// WithSettings returns an immutable client snapshot; live updates never mutate
// a client that another request is using.
func (c *Client) WithSettings(apiKey, contract string) *Client {
	copy := *c
	copy.apiKey, copy.contract = apiKey, contract
	return &copy
}

// WithPagination resumes a finite scan without losing rows sharing a timestamp.
func (c *Client) WithPagination(fingerprint string, untilMs int64) *Client {
	copy := *c
	copy.fingerprint = fingerprint
	copy.maxTimestampMs = untilMs
	return &copy
}

func (c *Client) TransfersSince(ctx context.Context, address string, minTimestampMs int64, limit int) (TransferPage, error) {
	return c.transfers(ctx, address, minTimestampMs, limit, false)
}

// TRXTransfersSince accepts successful native TransferContract calls only.
// Native amounts are SUN (one million SUN per TRX), already micro units.
func (c *Client) TRXTransfersSince(ctx context.Context, address string, minTimestampMs int64, limit int) (TransferPage, error) {
	return c.transfers(ctx, address, minTimestampMs, limit, true)
}

type chainRow struct {
	TransactionID string `json:"transaction_id"`
	TxID          string `json:"txID"`
	BlockTS       int64  `json:"block_timestamp"`
	From          string `json:"from"`
	To            string `json:"to"`
	Type          string `json:"type"`
	Value         string `json:"value"`
	TokenInfo     struct {
		Address  string `json:"address"`
		Decimals *int   `json:"decimals"`
	} `json:"token_info"`
	Ret []struct {
		Result string `json:"contractRet"`
	} `json:"ret"`
	RawData struct {
		Contract []struct {
			Type      string `json:"type"`
			Parameter struct {
				Value struct {
					Amount json.Number `json:"amount"`
					Owner  string      `json:"owner_address"`
					To     string      `json:"to_address"`
				} `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	} `json:"raw_data"`
}

func (c *Client) transfers(ctx context.Context, address string, minTimestampMs int64, limit int, native bool) (TransferPage, error) {
	if address == "" {
		return TransferPage{}, fmt.Errorf("tron: no address to watch")
	}
	if !native && c.contract == "" {
		return TransferPage{}, fmt.Errorf("tron: no token contract to watch")
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	query := url.Values{"only_confirmed": {"true"}, "limit": {strconv.Itoa(limit)}, "order_by": {"block_timestamp,asc"}}
	path := "/transactions/trc20"
	if native {
		path = "/transactions"
		query.Set("only_to", "true")
	} else {
		query.Set("contract_address", c.contract)
	}
	if minTimestampMs > 0 {
		query.Set("min_timestamp", strconv.FormatInt(minTimestampMs, 10))
	}
	if c.fingerprint != "" {
		query.Set("fingerprint", c.fingerprint)
	}
	if c.maxTimestampMs > 0 {
		query.Set("max_timestamp", strconv.FormatInt(c.maxTimestampMs, 10))
	}
	page := TransferPage{Transfers: []Transfer{}}
	seen := map[string]bool{c.fingerprint: true}
	for batch := 0; batch < 5; batch++ {
		endpoint := c.baseURL + "/v1/accounts/" + url.PathEscape(address) + path + "?" + query.Encode()
		body, err := c.fetch(ctx, endpoint)
		if err != nil {
			if batch > 0 {
				page.NextFingerprint = query.Get("fingerprint")
				return page, nil
			}
			return TransferPage{}, err
		}
		var payload struct {
			Success *bool      `json:"success"`
			Data    []chainRow `json:"data"`
			Meta    struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return TransferPage{}, fmt.Errorf("tron: invalid transfer response")
		}
		if payload.Data == nil {
			return TransferPage{}, fmt.Errorf("tron: missing transfer data")
		}
		if payload.Success != nil && !*payload.Success {
			return TransferPage{}, fmt.Errorf("tron: provider rejected transfer query")
		}
		for _, row := range payload.Data {
			if row.BlockTS <= 0 {
				return TransferPage{}, fmt.Errorf("tron: missing or invalid transfer timestamp")
			}
			if c.maxTimestampMs > 0 && row.BlockTS > c.maxTimestampMs {
				continue
			}
			if row.BlockTS > page.NewestRawMs {
				page.NewestRawMs = row.BlockTS
			}
			var transfer Transfer
			if native {
				if len(row.Ret) == 0 || len(row.RawData.Contract) == 0 {
					return TransferPage{}, fmt.Errorf("tron: incomplete native transfer metadata")
				}
				if row.Ret[0].Result == "" || row.RawData.Contract[0].Type == "" {
					return TransferPage{}, fmt.Errorf("tron: incomplete native transfer result")
				}
				if len(row.Ret) != 1 || row.Ret[0].Result != "SUCCESS" || len(row.RawData.Contract) != 1 {
					continue
				}
				contract := row.RawData.Contract[0]
				if contract.Type != "TransferContract" {
					continue
				}
				value := contract.Parameter.Value
				to, ok := NormalizeAddress(value.To)
				if !ok {
					return TransferPage{}, fmt.Errorf("tron: invalid native transfer recipient")
				}
				if to != address {
					continue
				}
				amount, err := strconv.ParseInt(string(value.Amount), 10, 64)
				if err != nil {
					return TransferPage{}, fmt.Errorf("tron: invalid native transfer amount")
				}
				if amount < 0 {
					return TransferPage{}, fmt.Errorf("tron: negative native transfer amount")
				}
				if amount <= 0 {
					continue
				}
				from, _ := NormalizeAddress(value.Owner)
				transfer = Transfer{TxID: row.TxID, From: from, To: to, AmountMicro: amount, BlockTSMs: row.BlockTS}
			} else {
				if row.Type != "" && row.Type != "Transfer" {
					continue
				}
				// Base58 addresses are case sensitive; EqualFold can credit another wallet.
				if row.To == "" {
					return TransferPage{}, fmt.Errorf("tron: missing transfer recipient")
				}
				if row.To != address {
					continue
				}
				// A missing field is a provider fault, not evidence of a foreign
				// payment. Completing this page would retire unpaid order windows
				// before the provider can return the repaired metadata.
				if row.TokenInfo.Address == "" {
					return TransferPage{}, fmt.Errorf("tron: missing transfer contract")
				}
				if row.TokenInfo.Address != c.contract {
					continue
				}
				if row.TokenInfo.Decimals == nil {
					return TransferPage{}, fmt.Errorf("tron: missing transfer precision")
				}
				if *row.TokenInfo.Decimals != microDecimals {
					continue
				}
				if row.Value == "" {
					return TransferPage{}, fmt.Errorf("tron: missing transfer amount")
				}
				amount, ok := toMicro(row.Value, microDecimals)
				if !ok {
					return TransferPage{}, fmt.Errorf("tron: invalid token transfer amount")
				}
				if amount <= 0 {
					continue
				}
				transfer = Transfer{TxID: row.TransactionID, From: row.From, To: row.To, AmountMicro: amount, BlockTSMs: row.BlockTS}
			}
			if transfer.TxID == "" {
				return TransferPage{}, fmt.Errorf("tron: missing transfer identity")
			}
			page.Transfers = append(page.Transfers, transfer)
		}
		fingerprint := payload.Meta.Fingerprint
		if fingerprint == "" {
			sort.SliceStable(page.Transfers, func(i, j int) bool { return page.Transfers[i].BlockTSMs < page.Transfers[j].BlockTSMs })
			return page, nil
		}
		// Never follow provider-supplied links: they could exfiltrate the API key.
		if seen[fingerprint] {
			return TransferPage{}, fmt.Errorf("tron: repeated pagination fingerprint")
		}
		seen[fingerprint] = true
		query.Set("fingerprint", fingerprint)
	}
	// Return a bounded batch and continuation instead of starving later pages.
	page.NextFingerprint = query.Get("fingerprint")
	sort.SliceStable(page.Transfers, func(i, j int) bool { return page.Transfers[i].BlockTSMs < page.Transfers[j].BlockTSMs })
	return page, nil
}

func (c *Client) fetch(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("tron: invalid endpoint")
	}
	if c.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tron: transfer request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A bad request alone is insufficient: authentication, filters and other
		// parameters can also fail. Never expose the provider body in errors.
		if req.URL.Query().Get("fingerprint") != "" && (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusGone) {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			var rejection struct {
				Error   string `json:"error"`
				Message string `json:"message"`
			}
			if readErr == nil && json.Unmarshal(body, &rejection) == nil &&
				(invalidFingerprintMessage(rejection.Error) || invalidFingerprintMessage(rejection.Message)) {
				return nil, ErrInvalidFingerprint
			}
		}
		return nil, fmt.Errorf("tron: transfer query status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return nil, fmt.Errorf("tron: invalid or oversized transfer response")
	}
	return body, nil
}

func invalidFingerprintMessage(message string) bool {
	message = strings.ToLower(message)
	for _, phrase := range []string{"invalid fingerprint", "expired fingerprint", "fingerprint is invalid", "fingerprint is expired", "fingerprint has expired", "fingerprint expired"} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// microDecimals is the scale every amount inside Prism is carried in: USDT's own
// six decimal places.
const microDecimals = 6

// toMicro converts a raw token amount, as the digit string the chain reports it,
// into the micro units every other part of Prism counts in.
//
// The arithmetic walks the string rather than an int64 because a token with more
// decimals than USDT overflows int64 long before it overflows a wallet: 100 of an
// 18-decimal token is 10^20 raw units, which ParseInt refuses. It answers false
// when the value cannot be represented exactly, so the caller can leave the row
// alone instead of crediting a number the chain never moved.
func toMicro(raw string, decimals int) (int64, bool) {
	if decimals < 0 || decimals > 24 {
		return 0, false
	}
	digits := strings.TrimSpace(raw)
	if digits == "" {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			// A token amount is an unsigned integer written in base ten;
			// anything else is not an amount this scanner may credit.
			return 0, false
		}
	}
	if decimals <= microDecimals {
		digits += strings.Repeat("0", microDecimals-decimals)
	} else {
		cut := decimals - microDecimals
		if len(digits) <= cut {
			// Below one micro unit. Zero is exact; anything else is not.
			if strings.Trim(digits, "0") == "" {
				return 0, true
			}
			return 0, false
		}
		if tail := digits[len(digits)-cut:]; strings.Trim(tail, "0") != "" {
			return 0, false
		}
		digits = digits[:len(digits)-cut]
	}
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
