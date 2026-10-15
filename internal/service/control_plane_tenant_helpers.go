package service

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"prism/internal/keys"
	"prism/internal/model"
	"prism/internal/state"
)

// Shared pieces of the multi-tenant surface: identifiers, the plan snapshot
// encoding the data plane reads, and the mapping from a repository error to the
// answer a client gets.

// newID returns a fresh identifier with a prefix naming what it identifies.
//
// A UUIDv7 without its dashes, so rows sort by creation time and the column is a
// plain text primary key. The prefix is what makes an id self-describing in a
// URL and in a log line.
func newID(prefix string) (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("service: generate %s id: %w", prefix, err)
	}
	return prefix + strings.ReplaceAll(id.String(), "-", ""), nil
}

// inviteCodeAlphabet omits the characters people confuse when reading a code off
// a screen: 0 and O, 1, I and L. Thirty-two symbols keep eight characters at
// forty bits of entropy, which is far beyond guessing at the rate registration
// allows.
const inviteCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// newInviteCode returns a code shaped XXXX-XXXX.
func newInviteCode() (string, error) {
	const groups, groupLen = 2, 4
	buf := make([]byte, groups*groupLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("service: generate invite code: %w", err)
	}

	var out strings.Builder
	for i, b := range buf {
		if i > 0 && i%groupLen == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(inviteCodeAlphabet[int(b)%len(inviteCodeAlphabet)])
	}
	return out.String(), nil
}

// planSnapshotJSON encodes a plan the way a subscription stores it.
func planSnapshotJSON(plan model.Plan) (string, error) {
	return keys.MarshalPlanSnapshot(plan)
}

// planFromSnapshotJSON decodes the plan a subscription carries.
//
// An empty snapshot is the zero plan rather than an error: a subscription
// without one still grants its window, and zero quotas mean unlimited.
func planFromSnapshotJSON(raw string) (model.Plan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return model.Plan{}, nil
	}
	var plan model.Plan
	if err := json.Unmarshal([]byte(trimmed), &plan); err != nil {
		return model.Plan{}, fmt.Errorf("service: decode plan snapshot: %w", err)
	}
	return plan, nil
}

// expiryFromDays turns a duration in days into an absolute time. Zero days
// means "no expiry" and comes back as zero.
func expiryFromDays(days int, nowNs int64) int64 {
	if days <= 0 {
		return 0
	}
	return nowNs + int64(days)*int64(24*time.Hour)
}

// remainingBytes reports the traffic left in the period, or nil when the plan
// does not limit traffic at all.
//
// The nil is the point: a client rendering "unlimited" and a client rendering
// "0 bytes left" must not be able to confuse the two.
func remainingBytes(trafficBytes, usedBytes int64) *int64 {
	if trafficBytes <= 0 {
		return nil
	}
	remaining := trafficBytes - usedBytes
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

// mapUserLookupError maps a failed user read onto the client's answer.
func mapUserLookupError(err error) *ServiceError {
	if errors.Is(err, state.ErrNotFound) {
		return notFound("user not found")
	}
	return internal("read user", err)
}

// mapUserWriteError maps a failed user write onto the client's answer.
func mapUserWriteError(err error) *ServiceError {
	if errors.Is(err, state.ErrConflict) {
		return conflict("a user with that username already exists")
	}
	if errors.Is(err, state.ErrNotFound) {
		return notFound("user not found")
	}
	return internal("write user", err)
}

// mapSubscriptionLookupError maps a failed subscription read onto the answer.
func mapSubscriptionLookupError(err error) *ServiceError {
	if errors.Is(err, state.ErrNotFound) {
		return notFound("subscription not found")
	}
	return internal("read subscription", err)
}

// mapInviteLookupError maps a failed invite read onto the client's answer.
func mapInviteLookupError(err error) *ServiceError {
	if errors.Is(err, state.ErrNotFound) {
		return notFound("invite not found")
	}
	return internal("read invite", err)
}
