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

// resolveRoutedOutbound resolves the platform, enforces the key's platform
// restriction, and then routes.
//
// This is the single convergence point of the forward proxy, the CONNECT
// tunnel, the SOCKS5 inbound and the reverse proxy, which is why the platform
// check lives here rather than in each entry point: there is exactly one place
// that can get it wrong.
//
// key is nil for the transitional global-token path and for deployments with
// authentication disabled. A nil key imposes no restriction, so those paths
// behave exactly as they did before access keys existed.
//
// The platform is resolved before the check because the key stores platform
// IDs, while the request names a platform. That costs one extra map lookup on
// the routed path, which is not measurable next to a proxy round trip.
func resolveRoutedOutbound(
	router *routing.Router,
	pool outbound.PoolAccessor,
	platformName string,
	account string,
	target string,
	key *keys.KeyState,
) (routedOutbound, *ProxyError) {
	if key != nil {
		platformID, err := router.ResolvePlatformID(platformName)
		if err != nil {
			return routedOutbound{}, mapRouteError(err)
		}
		if !key.AllowsPlatform(platformID) {
			return routedOutbound{}, ErrPlatformForbidden
		}
	}

	result, err := router.RouteRequest(platformName, account, target)
	if err != nil {
		return routedOutbound{}, mapRouteError(err)
	}

	entry, ok := pool.GetEntry(result.NodeHash)
	if !ok {
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
