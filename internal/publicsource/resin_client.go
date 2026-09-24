package publicsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// remoteDecodeLimit is a sanity cap on a success body. It is deliberately
	// generous because the subscription create/update endpoints echo back the
	// subscription, including the full content we just uploaded: a 13 MiB
	// payload legitimately produces a ~13 MiB response. The body is decoded as a
	// stream, so a large response costs no extra memory.
	remoteDecodeLimit = 256 << 20
	// errorBodyLimit bounds how much of an error response is buffered to build a
	// message from it.
	errorBodyLimit   = 64 << 10
	listPageSize     = 1000
	maxListedRecords = 20000
)

type ResinClient struct {
	baseURL    string
	adminToken string
	client     *http.Client
}

type remoteSubscriptionPage struct {
	Items []remoteSubscription `json:"items"`
	Total int                  `json:"total"`
}

type remoteSubscription struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
}

// NewResinClient builds an admin-API client. Redirects are never followed so
// the admin token cannot be replayed to a redirect target.
func NewResinClient(baseURL, adminToken string, httpClient *http.Client) (*ResinClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("resin client: base URL must be an absolute http(s) URL")
	}
	if parsed.User != nil {
		return nil, errors.New("resin client: base URL must not contain credentials")
	}
	if strings.TrimSpace(adminToken) == "" {
		return nil, errors.New("resin client: admin token must not be empty")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &ResinClient{baseURL: baseURL, adminToken: adminToken, client: httpClient}, nil
}

// LocalSubscriptionOptions are the non-content settings the sync applies to the
// subscription it manages. They are applied on both create and update, because a
// subscription that already exists would otherwise keep its server-side defaults
// forever.
type LocalSubscriptionOptions struct {
	// Ephemeral marks the subscription for the periodic cleanup sweep, which is
	// what removes nodes that have stopped working.
	Ephemeral bool
	// IncrementalAliveNodes keeps existing healthy nodes when the refreshed
	// content no longer lists them, instead of replacing the held set wholesale.
	// For a public list that churns this is what stops a working node from being
	// dropped just because it rotated off the list for one cycle.
	IncrementalAliveNodes bool
	// EphemeralNodeEvictDelay is how long a node may stay circuit-open before the
	// cleanup sweep evicts it. Only meaningful when Ephemeral is set.
	EphemeralNodeEvictDelay time.Duration
}

// UpsertLocalSubscription creates or updates one local subscription identified by
// name and triggers an immediate refresh. It never changes the `enabled` flag of
// an existing subscription, so an operator can pause syncing by disabling it.
func (c *ResinClient) UpsertLocalSubscription(
	ctx context.Context,
	name, content string,
	updateInterval time.Duration,
	opts LocalSubscriptionOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("resin client: subscription name must not be empty")
	}
	if strings.TrimSpace(content) == "" {
		return errors.New("resin client: subscription content must not be empty")
	}
	if updateInterval < 30*time.Second {
		return errors.New("resin client: update interval must be at least 30s")
	}
	if opts.EphemeralNodeEvictDelay < 0 {
		return errors.New("resin client: ephemeral node evict delay must not be negative")
	}

	existing, err := c.findLocalSubscription(ctx, name)
	if err != nil {
		return err
	}

	if existing.ID == "" {
		var created remoteSubscription
		if err := c.doJSON(ctx, http.MethodPost, "/api/v1/subscriptions", map[string]any{
			"name":                       name,
			"source_type":                "local",
			"content":                    content,
			"update_interval":            updateInterval.String(),
			"enabled":                    true,
			"ephemeral":                  opts.Ephemeral,
			"incremental_alive_nodes":    opts.IncrementalAliveNodes,
			"ephemeral_node_evict_delay": opts.EphemeralNodeEvictDelay.String(),
		}, &created); err != nil {
			return fmt.Errorf("resin client: create subscription: %w", err)
		}
		if created.ID == "" {
			return errors.New("resin client: create subscription returned empty id")
		}
		existing = &created
	} else {
		if err := c.doJSON(ctx, http.MethodPatch, "/api/v1/subscriptions/"+url.PathEscape(existing.ID), map[string]any{
			"content":                    content,
			"update_interval":            updateInterval.String(),
			"ephemeral":                  opts.Ephemeral,
			"incremental_alive_nodes":    opts.IncrementalAliveNodes,
			"ephemeral_node_evict_delay": opts.EphemeralNodeEvictDelay.String(),
		}, nil); err != nil {
			return fmt.Errorf("resin client: update subscription: %w", err)
		}
	}

	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/subscriptions/"+url.PathEscape(existing.ID)+"/actions/refresh", nil, nil); err != nil {
		return fmt.Errorf("resin client: refresh subscription: %w", err)
	}
	return nil
}

// findLocalSubscription locates an exact name+local match, paging through
// results so a >1 page listing cannot cause a duplicate subscription.
func (c *ResinClient) findLocalSubscription(ctx context.Context, name string) (*remoteSubscription, error) {
	for offset := 0; offset < maxListedRecords; offset += listPageSize {
		path := fmt.Sprintf("/api/v1/subscriptions?limit=%d&offset=%d&keyword=%s",
			listPageSize, offset, url.QueryEscape(name))
		var page remoteSubscriptionPage
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &page); err != nil {
			return nil, fmt.Errorf("resin client: list subscriptions: %w", err)
		}
		for i := range page.Items {
			item := page.Items[i]
			if item.Name == name && item.SourceType == "local" {
				return &item, nil
			}
		}
		if len(page.Items) == 0 || len(page.Items) < listPageSize {
			break
		}
	}
	return &remoteSubscription{}, nil
}

func (c *ResinClient) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.adminToken)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Error responses are small: read a bounded amount to summarise them.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return fmt.Errorf("HTTP %s: %s", resp.Status, summarizeError(data))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodyLimit))
		return nil
	}
	// Stream the success body straight into out rather than buffering it, so a
	// response that carries the payload back does not need a second copy in
	// memory and cannot trip a fixed buffer limit.
	decoder := json.NewDecoder(io.LimitReader(resp.Body, remoteDecodeLimit))
	if err := decoder.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// summarizeError extracts Resin's structured error code when present instead of
// echoing an arbitrary server body into logs.
func summarizeError(data []byte) string {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Code != "" {
		message := strings.TrimSpace(envelope.Error.Message)
		if len(message) > 200 {
			message = message[:200]
		}
		if message == "" {
			return envelope.Error.Code
		}
		return envelope.Error.Code + ": " + message
	}
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 200 {
		trimmed = trimmed[:200]
	}
	return trimmed
}
