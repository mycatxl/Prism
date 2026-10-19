// Package tron reads TRC20 transfers from TronGrid.
//
// It is read-only and holds no key: the receiving address is the operator's own
// wallet and the server's only job is to notice money that has already arrived.
// Every query targets the solidified endpoint, whose result cannot be rolled
// back by a chain reorganisation - crediting from head state would let a
// credited deposit disappear.
package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Transfer is one confirmed TRC20 transfer to the watched address, already
// decoded by TronGrid.
type Transfer struct {
	TxID        string
	From        string
	To          string
	AmountMicro int64
	// BlockTSMs is the block's own timestamp in milliseconds. Crediting is
	// judged on this, never on when the poller happened to see the transfer.
	BlockTSMs int64
}

// TransferPage is one server page of the account's TRC20 history together with
// the block time of its newest row *before* any filtering.
//
// The deposit cursor advances on NewestRawMs rather than on the newest
// surviving transfer. A page holds rows the scanner deliberately drops -
// transfers out of the watched address, other event types, values it cannot
// decode - and a cursor that advanced only past surviving rows would stop
// moving whenever a whole page survived as nothing. The scan would then re-read
// the same page for ever and everything behind it would stay invisible.
type TransferPage struct {
	Transfers   []Transfer
	NewestRawMs int64
}

// Client talks to TronGrid's V1 account API.
type Client struct {
	baseURL  string
	apiKey   string
	contract string
	http     *http.Client
}

// NewClient builds a client for one contract.
//
// The API key is optional but TronGrid rate-limits anonymous callers hard enough
// that a poller needs one; an empty key is therefore allowed, not encouraged.
func NewClient(baseURL, apiKey, contract string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		contract: contract,
		http: &http.Client{
			// A poller must not hang: a stalled request would silently stop
			// watching the chain, which looks exactly like "nobody paid".
			Timeout: 30 * time.Second,
		},
	}
}

// TransfersSince returns confirmed transfers to address with a block time at or
// after minTimestampMs, oldest first, together with the raw page's own newest
// timestamp.
//
// min_timestamp is inclusive on TronGrid's side, so a record exactly on the
// watermark comes back again; the transaction id makes that harmless. The
// endpoint has no block-height filter, which is why the watermark is a
// timestamp.
//
// order_by is requested explicitly because TronGrid defaults to
// block_timestamp,desc. The watermark is the block time of the newest transfer
// handled, so a descending page means the cursor settles on the OLDEST row of
// that page, and a backlog larger than one page then leaves everything older
// than the newest page unreachable for good. The rows are sorted here as well,
// so the "oldest first" contract holds even against an endpoint that ignores
// the parameter.
func (c *Client) TransfersSince(ctx context.Context, address string, minTimestampMs int64, limit int) (TransferPage, error) {
	if address == "" {
		return TransferPage{}, fmt.Errorf("tron: no address to watch")
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}

	query := url.Values{}
	query.Set("only_confirmed", "true")
	query.Set("limit", strconv.Itoa(limit))
	query.Set("contract_address", c.contract)
	query.Set("order_by", "block_timestamp,asc")
	if minTimestampMs > 0 {
		query.Set("min_timestamp", strconv.FormatInt(minTimestampMs, 10))
	}
	endpoint := fmt.Sprintf("%s/v1/accounts/%s/transactions/trc20?%s",
		c.baseURL, url.PathEscape(address), query.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TransferPage{}, fmt.Errorf("tron: build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return TransferPage{}, fmt.Errorf("tron: query transfers: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return TransferPage{}, fmt.Errorf("tron: read transfers: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return TransferPage{}, fmt.Errorf("tron: query transfers: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var payload struct {
		Data []struct {
			TransactionID string `json:"transaction_id"`
			BlockTS       int64  `json:"block_timestamp"`
			From          string `json:"from"`
			To            string `json:"to"`
			Type          string `json:"type"`
			Value         string `json:"value"`
			TokenInfo     struct {
				Address  string `json:"address"`
				Decimals *int   `json:"decimals"`
			} `json:"token_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return TransferPage{}, fmt.Errorf("tron: decode transfers: %w", err)
	}

	page := TransferPage{Transfers: make([]Transfer, 0, len(payload.Data))}
	for _, row := range payload.Data {
		// The raw watermark moves on every row the page returns, including the
		// ones dropped below: that is what keeps the scan from freezing on a
		// page whose rows all belong to somebody else.
		if row.BlockTS > page.NewestRawMs {
			page.NewestRawMs = row.BlockTS
		}
		if row.Type != "" && row.Type != "Transfer" {
			continue
		}
		if !strings.EqualFold(row.To, address) {
			continue
		}
		if row.TokenInfo.Address != "" && !strings.EqualFold(row.TokenInfo.Address, c.contract) {
			continue
		}
		// The scale of the amount is the token's, not ours: a contract that
		// reports 18 decimals sends values 10^12 times larger than the micro
		// units every other part of Prism counts in. A token_info without a
		// scale is the configured USDT contract, whose scale is the micro unit.
		decimals := microDecimals
		if row.TokenInfo.Decimals != nil {
			decimals = *row.TokenInfo.Decimals
		}
		movable, ok := toMicro(row.Value, decimals)
		if !ok {
			// Either the value is too large to represent or the token carries
			// finer precision than one micro unit. Crediting a rounded number
			// would credit an amount the chain never transferred, so the row is
			// left alone.
			continue
		}
		page.Transfers = append(page.Transfers, Transfer{
			TxID:        row.TransactionID,
			From:        row.From,
			To:          row.To,
			AmountMicro: movable,
			BlockTSMs:   row.BlockTS,
		})
	}
	sort.SliceStable(page.Transfers, func(i, j int) bool { return page.Transfers[i].BlockTSMs < page.Transfers[j].BlockTSMs })
	return page, nil
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
