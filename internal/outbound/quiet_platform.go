package outbound

import (
	"context"
	"net/netip"
	"os"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
)

// quietPlatformInterface replaces sing-box's netlink default-interface monitor
// with a no-op one.
//
// Why this exists: on Linux, `box.Start()` creates a netlink network monitor
// whose callback spawns a goroutine that reads `NetworkManager.started`, while
// `NetworkManager.Start` writes that field without synchronization
// (sing-box v1.14.0 route/network.go:220 vs :574). `go test -race` therefore
// reports a data race for every embedded box on Linux, which would make
// `make verify` fail for reasons outside Prism's control.
//
// Prism never relies on interface auto-detection: node outbounds are dialed
// through `adapter.Outbound.DialContext` without `auto_detect_interface` or
// `bind_interface`. Tests therefore install this stub; the production runtime
// keeps sing-box's real monitor.
type quietPlatformInterface struct {
	monitor *quietInterfaceMonitor
}

func newQuietPlatformInterface() *quietPlatformInterface {
	return &quietPlatformInterface{monitor: new(quietInterfaceMonitor)}
}

func (p *quietPlatformInterface) Initialize(adapter.NetworkManager) error { return nil }

func (p *quietPlatformInterface) UsePlatformAutoDetectInterfaceControl() bool { return false }

func (p *quietPlatformInterface) AutoDetectInterfaceControl(int) error { return os.ErrInvalid }

func (p *quietPlatformInterface) UsePlatformInterface() bool { return false }

func (p *quietPlatformInterface) OpenInterface(*tun.Options, option.TunPlatformOptions) (tun.Tun, error) {
	return nil, os.ErrInvalid
}

func (p *quietPlatformInterface) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }

func (p *quietPlatformInterface) UsePlatformDefaultInterfaceMonitor() bool { return true }

func (p *quietPlatformInterface) CreateDefaultInterfaceMonitor(logger.Logger) tun.DefaultInterfaceMonitor {
	return p.monitor
}

func (p *quietPlatformInterface) UsePlatformNetworkInterfaces() bool { return false }

func (p *quietPlatformInterface) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	return nil, os.ErrInvalid
}

func (p *quietPlatformInterface) UnderNetworkExtension() bool { return false }

func (p *quietPlatformInterface) NetworkExtensionIncludeAllNetworks() bool { return false }

func (p *quietPlatformInterface) ClearDNSCache() {}

func (p *quietPlatformInterface) RequestPermissionForWIFIState() error { return nil }

func (p *quietPlatformInterface) ReadWIFIState(context.Context) adapter.WIFIState {
	return adapter.WIFIState{}
}

func (p *quietPlatformInterface) UsePlatformConnectionOwnerFinder() bool { return false }

func (p *quietPlatformInterface) FindConnectionOwner(*adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	return nil, os.ErrInvalid
}

func (p *quietPlatformInterface) UsePlatformWIFIMonitor() bool { return true }

func (p *quietPlatformInterface) UsePlatformNotification() bool { return false }

func (p *quietPlatformInterface) SendNotification(*adapter.Notification) error { return os.ErrInvalid }

func (p *quietPlatformInterface) CancelNotification(string, int32) error { return os.ErrInvalid }

func (p *quietPlatformInterface) MyInterfaceAddress() []netip.Addr { return nil }

func (p *quietPlatformInterface) UsePlatformNeighborResolver() bool { return false }

func (p *quietPlatformInterface) StartNeighborMonitor(adapter.NeighborUpdateListener) error {
	return os.ErrInvalid
}

func (p *quietPlatformInterface) CloseNeighborMonitor(adapter.NeighborUpdateListener) error {
	return os.ErrInvalid
}

func (p *quietPlatformInterface) UsePlatformShell() bool { return false }

func (p *quietPlatformInterface) CheckPlatformShell() error { return os.ErrInvalid }

func (p *quietPlatformInterface) OpenShellSession(*adapter.PlatformUser, string, []string, string, int32, int32) (adapter.ShellSession, error) {
	return nil, os.ErrInvalid
}

func (p *quietPlatformInterface) LookupUser(string) (*adapter.PlatformUser, error) {
	return nil, os.ErrInvalid
}

func (p *quietPlatformInterface) LookupSFTPServer() (string, error) { return "", os.ErrInvalid }

func (p *quietPlatformInterface) ReadSystemSSHHostKey() ([]byte, error) { return nil, os.ErrInvalid }

func (p *quietPlatformInterface) TailscaleHostname() string { return "" }

func (p *quietPlatformInterface) UsePlatformBridge() bool { return false }

func (p *quietPlatformInterface) CreateBridge(adapter.BridgeOptions) (adapter.BridgeSession, error) {
	return nil, os.ErrInvalid
}

// quietInterfaceMonitor is a default-interface monitor that never reports an
// interface and therefore never invokes a network-manager callback.
type quietInterfaceMonitor struct{}

func (m *quietInterfaceMonitor) Start() error { return nil }

func (m *quietInterfaceMonitor) Close() error { return nil }

func (m *quietInterfaceMonitor) DefaultInterface() *control.Interface { return nil }

func (m *quietInterfaceMonitor) OverrideAndroidVPN() bool { return false }

func (m *quietInterfaceMonitor) AndroidVPNEnabled() bool { return false }

func (m *quietInterfaceMonitor) RegisterCallback(tun.DefaultInterfaceUpdateCallback) *list.Element[tun.DefaultInterfaceUpdateCallback] {
	return nil
}

func (m *quietInterfaceMonitor) UnregisterCallback(*list.Element[tun.DefaultInterfaceUpdateCallback]) {
}

func (m *quietInterfaceMonitor) RegisterMyInterface(string) {}

func (m *quietInterfaceMonitor) MyInterface() string { return "" }

func (m *quietInterfaceMonitor) MyInterfaces() []string { return nil }
