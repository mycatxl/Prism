package netutil

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	M "github.com/sagernet/sing/common/metadata"
)

// ParseDestination validates an authority before converting its port to uint16.
// The engine's permissive parser silently truncates ports outside that range.
func ParseDestination(authority string) (M.Socksaddr, error) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || port == "" || host == "." || host == ".." || strings.ContainsAny(host, "/\\@?#[]") {
		return M.Socksaddr{}, fmt.Errorf("invalid destination authority")
	}
	for _, c := range host {
		if c <= ' ' || c == 127 {
			return M.Socksaddr{}, fmt.Errorf("invalid destination host")
		}
	}
	if strings.HasPrefix(authority, "[") || strings.Contains(host, ":") {
		if _, err := netip.ParseAddr(host); err != nil {
			return M.Socksaddr{}, fmt.Errorf("invalid destination address")
		}
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return M.Socksaddr{}, fmt.Errorf("invalid destination port")
		}
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return M.Socksaddr{}, fmt.Errorf("invalid destination port")
	}
	return M.ParseSocksaddrHostPort(host, uint16(n)), nil
}
