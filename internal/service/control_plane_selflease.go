package service

import (
	"strings"
	"sync"
	"time"

	"prism/internal/platform"
	"prism/internal/routing"
	"prism/internal/safego"
)

// A user's own view of their sticky leases.
//
// Leases live in the routing table in memory, not in a table: they are the
// data plane's own state, and the console reads them through the router the
// same way the operator's lease page does. There is therefore no query by user
// - the only iteration the router offers is per platform - so this walks the
// platforms and filters. A deployment has a handful of platforms and a user has
// a handful of leases, which is why that is the right shape rather than a new
// index.

// SelfLease is one lease as its owner sees it.
//
// It is narrower than the operator's LeaseResponse on purpose: node_hash is the
// router's internal identity of an exit, and the user's own panel has no use for
// it. The platform is named rather than identified, because the console shows
// names and a user never learns internal ids.
type SelfLease struct {
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	Account      string `json:"account"`
	NodeTag      string `json:"node_tag"`
	EgressIP     string `json:"egress_ip"`
	Expiry       string `json:"expiry"`
	LastAccessed string `json:"last_accessed"`
}

// SelfLeases lists the caller's leases across every platform.
//
// A lease with no owner is the single-tenant one (the transitional global proxy
// token) and is never returned here: it belongs to nobody, and showing it to a
// signed-in user would be showing them somebody else's exit.
func (s *ControlPlaneService) SelfLeases(userID string) ([]SelfLease, error) {
	if s == nil || s.Engine == nil {
		return nil, internal("no state engine", nil)
	}
	if strings.TrimSpace(userID) == "" {
		return nil, invalidArg("user: required")
	}
	if s.Pool == nil || s.Router == nil {
		return []SelfLease{}, nil
	}

	result := []SelfLease{}
	s.Pool.RangePlatforms(func(plat *platform.Platform) bool {
		if plat == nil {
			return true
		}
		s.Router.RangeLeases(plat.ID, func(ownerID, account string, lease routing.Lease) bool {
			if ownerID != userID {
				return true
			}
			result = append(result, SelfLease{
				PlatformID:   plat.ID,
				PlatformName: plat.Name,
				Account:      account,
				NodeTag:      s.resolveLeaseNodeTag(lease.NodeHash),
				EgressIP:     lease.EgressIP.String(),
				Expiry:       time.Unix(0, lease.ExpiryNs).UTC().Format(time.RFC3339Nano),
				LastAccessed: time.Unix(0, lease.LastAccessedNs).UTC().Format(time.RFC3339Nano),
			})
			return true
		})
		return true
	})
	return result, nil
}

// ReleaseSelfLease drops one of the caller's own leases.
//
// The owner is the session's, never the path's, so an account name learnt
// elsewhere removes nothing: the delete is keyed by (platform, owner, account)
// and the owner comes from the caller. A lease that is not there is reported as
// not found, which is the same answer as one that belongs to somebody else.
func (s *ControlPlaneService) ReleaseSelfLease(userID, platformID, account string) error {
	if s == nil || s.Engine == nil {
		return internal("no state engine", nil)
	}
	if strings.TrimSpace(userID) == "" {
		return invalidArg("user: required")
	}
	platformID = strings.TrimSpace(platformID)
	account = strings.TrimSpace(account)
	if platformID == "" {
		return invalidArg("platform: required")
	}
	if account == "" {
		return invalidArg("account: required")
	}
	if s.Pool == nil || s.Router == nil {
		return notFound("lease not found")
	}
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return notFound("lease not found")
	}
	if !s.Router.DeleteLease(platformID, userID, account) {
		return notFound("lease not found")
	}
	return nil
}

// renewalSweepInterval is how often the renewal sweep runs.
//
// A minute matches the period rollover, and both are cheap: the sweep is one
// indexed read of the subscriptions that opted in and are close to expiring.
// Running it this often is what keeps the lead time meaningful - the whole point
// is to renew before the window closes, not after.
const renewalSweepInterval = time.Minute

// StartRenewalSweeper runs RenewDueSubscriptions every interval until the
// returned function is called.
//
// The loop never stops on an error, for the same reason the expiry sweep does
// not: a background component that dies on a transient database error leaves
// every opted-in subscription unrenewed forever. The stop function is
// idempotent and waits for the goroutine, so once it returns no further write
// can reach the database - which is why the shutdown sequence stops this before
// the database is closed.
func StartRenewalSweeper(cp *ControlPlaneService, interval time.Duration, logf func(string, ...any)) func() {
	if interval <= 0 {
		interval = renewalSweepInterval
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// A panic inside one pass must not end the loop: the next
				// tick retries, exactly as it does after a failed pass.
				safego.Run("the subscription renewal sweep", func() {
					result, err := cp.RenewDueSubscriptions(time.Now().UnixNano())
					if err != nil {
						logf("subscription renewal sweep failed: %v", err)
						return
					}
					if result.Renewed > 0 || result.ShortOfFunds > 0 || result.Failed > 0 {
						logf("subscriptions renewed: %d charged, %d short of funds, %d failed",
							result.Renewed, result.ShortOfFunds, result.Failed)
					}
				})
			}
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
