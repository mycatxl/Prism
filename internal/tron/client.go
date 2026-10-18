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
// after minTimestampMs, oldest first.
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
func (c *Client) TransfersSince(ctx context.Context, address string, minTimestampMs int64, limit int) ([]Transfer, error) {
	if address == "" {
		return nil, fmt.Errorf("tron: no address to watch")
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
		return nil, fmt.Errorf("tron: build request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tron: query transfers: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("tron: read transfers: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tron: query transfers: status %d: %s", resp.StatusCode, truncate(string(body), 200))
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
				Decimals int    `json:"decimals"`
			} `json:"token_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("tron: decode transfers: %w", err)
	}

	out := make([]Transfer, 0, len(payload.Data))
	for _, row := range payload.Data {
		if row.Type != "" && row.Type != "Transfer" {
			continue
		}
		if !strings.EqualFold(row.To, address) {
			continue
		}
		if row.TokenInfo.Address != "" && !strings.EqualFold(row.TokenInfo.Address, c.contract) {
			continue
		}
		amount, err := strconv.ParseInt(row.Value, 10, 64)
		if err != nil {
			// A value that does not fit int64 is not a plausible USDT amount;
			// skipping it is safer than saturating to a wrong number.
			continue
		}
		out = append(out, Transfer{
			TxID:        row.TransactionID,
			From:        row.From,
			To:          row.To,
			AmountMicro: amount,
			BlockTSMs:   row.BlockTS,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].BlockTSMs < out[j].BlockTSMs })
	return out, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
