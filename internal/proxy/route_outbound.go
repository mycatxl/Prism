package proxy

import (
	"github.com/sagernet/sing-box/adapter"
	"prism/internal/keys"
	"prism/internal/outbound"
	"prism/internal/routing"
)

type routedOutbound struct {
	Route    routing.RouteResult
	Outbound adapter.Outbound
}

// authorizeBypassPlatform enforces a restricted key's platform scope before an
// operator-configured direct bypass. Bypass targets do not create routing
// leases, but the credential still names a platform and must not gain a second,
// unrestricted authorization path merely because its target is dialled locally.
// Unrestricted keys keep the historical direct-only behavior and do not require
// a router just to resolve a platform they are allowed to use universally.
func authorizeBypassPlatform(router *routing.Router, platformName string, key *keys.KeyState) *ProxyError {
	if router != nil && router.PrivacyPolicyEnabled() {
		return ErrNoAvailableNodes
	}
	// Local direct egress has no assessed node identity. It cannot satisfy a
	// plan's tier restriction, even when an operator configured a bypass.
	if key != nil && len(key.Plan.IPTiers) > 0 {
		return ErrPlatformForbidden
	}
	if key == nil || len(key.EffectivePlatforms()) == 0 {
		return nil
	}
	if router == nil {
		return ErrPlatformForbidden
	}
	platformID, err := router.ResolvePlatformID(platformName)
	if err != nil {
		return mapRouteError(err)
	}
	if !key.AllowsPlatform(platformID) {
		return ErrPlatformForbidden
	}
	return nil
}

// resolveRoutedOutbound resolves the platform, enforces the key's platform
// restriction, and then routes.
//
// Routed requests from the forward proxy, CONNECT tunnel, SOCKS5 inbound and
// reverse proxy converge here. Direct bypasses use authorizeBypassPlatform for
// the same key-scope check without creating a routing lease.
//
// key is the authenticated AccessKey. A nil key is retained as a defensive
// guard for internal callers and imposes no platform restriction there.
//
// The platform name is resolved once; authorization and routing use that same
// ID even if an operator concurrently reassigns the name.
func resolveRoutedOutbound(
	router *routing.Router,
	pool outbound.PoolAccessor,
	platformName string,
	account string,
	target string,
	key *keys.KeyState,
) (routedOutbound, *ProxyError) {
	platformID, err := router.ResolvePlatformID(platformName)
	if err != nil {
		return routedOutbound{}, mapRouteError(err)
	}
	if key != nil && !key.AllowsPlatform(platformID) {
		return routedOutbound{}, ErrPlatformForbidden
	}

	// The key's owner, when there is one, is part of the lease identity: two
	// tenants that both call their account "default" must not share an egress
	// lease.
	userID := ""
	var tiers []string
	if key != nil {
		userID = key.UserID
		tiers = key.Plan.IPTiers
	}
	result, err := router.RouteRequestForPlatform(platformID, userID, account, target, tiers)
	if err != nil {
		return routedOutbound{}, mapRouteError(err)
	}

	entry, ok := pool.GetEntry(result.NodeHash)
	if !ok || !entry.PrivacyAllowed() || !router.RouteStillValid(result, tiers) {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	obPtr := entry.Outbound.Load()
	if obPtr == nil {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	return routedOutbound{
		Route:    result,
		Outbound: *obPtr,
	}, nil
}
