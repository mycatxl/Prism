package proxy

import "strings"

// parseV1PlatformAccountIdentity parses V1 identity segment:
// "Platform.Account" (preferred) or "Platform:Account".
func parseV1PlatformAccountIdentity(identity string) (string, string) {
	dot := strings.IndexByte(identity, '.')
	colon := strings.IndexByte(identity, ':')
	switch {
	case dot < 0 && colon < 0:
		return identity, ""
	case dot < 0:
		return identity[:colon], identity[colon+1:]
	case colon < 0:
		return identity[:dot], identity[dot+1:]
	case dot < colon:
		return identity[:dot], identity[dot+1:]
	default:
		return identity[:colon], identity[colon+1:]
	}
}
