// Package landiscovery advertises the server on the local network using
// mDNS / DNS-SD (Bonjour) so clients can find it without typing an address.
//
// Service type: _mediastorm._tcp (domain local.), port = HTTP port.
// TXT records (all optional except txtvers):
//
//	txtvers=1
//	version=<backend version>
//	name=<display name>
//	apiPath=<base path>/api
//	serverId=<remote-access Iroh endpoint id, 64 lowercase hex>  (when available)
//
// Advertising is best effort: when multicast is unavailable (for example a
// Docker bridge network) Start logs one line and returns a disabled
// advertiser; it never fails server start-up.
package landiscovery

import (
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/libp2p/zeroconf/v2"
)

const (
	// ServiceType is the DNS-SD service type clients browse for.
	ServiceType = "_mediastorm._tcp"
	// Domain is the mDNS domain.
	Domain = "local."
	// TXTVersion is the version of the TXT record layout.
	TXTVersion = "1"

	maxInstanceNameLen = 63
)

// Config describes what to advertise.
type Config struct {
	// Name is the human-readable instance name (default: host name).
	Name string
	// Port is the HTTP port the API listens on.
	Port int
	// Version is the backend version string.
	Version string
	// BasePath is the reverse-proxy path prefix ("" when none).
	BasePath string
	// ServerID is the remote-access identity; may be set later via SetServerID.
	ServerID string
}

type registration interface {
	SetText(text []string)
	Shutdown()
}

type registerFunc func(instance, service, domain string, port int, text []string, ifaces []net.Interface) (registration, error)

var defaultRegister registerFunc = func(instance, service, domain string, port int, text []string, ifaces []net.Interface) (registration, error) {
	// RegisterProxy with no fixed IPs answers A/AAAA queries with the
	// addresses of the interface each query arrived on. zeroconf.Register
	// would instead append ".local." to a host name that already ends in
	// ".local" (macOS), producing an unresolvable "host.local.local." target.
	server, err := zeroconf.RegisterProxy(instance, service, domain, port, hostLabel(), nil, text, ifaces)
	if err != nil {
		return nil, err
	}
	return server, nil
}

// hostLabel returns the machine's host name as a single DNS label
// ("Living-Room" for "Living-Room.local" or "Living-Room.example.com").
func hostLabel() string {
	host, err := os.Hostname()
	host = strings.TrimSpace(host)
	if err != nil || host == "" {
		return "mediastorm"
	}
	if label, _, ok := strings.Cut(host, "."); ok && label != "" {
		return label
	}
	return host
}

// Advertiser owns one mDNS registration. A nil or disabled Advertiser is safe
// to use; all methods become no-ops.
type Advertiser struct {
	mu     sync.Mutex
	cfg    Config
	server registration
}

// Start registers the service. It never returns an error: failures are
// logged once and yield an inactive advertiser.
func Start(cfg Config) *Advertiser {
	return start(cfg, defaultRegister, usableInterfaces)
}

func start(cfg Config, register registerFunc, interfaces func() []net.Interface) (adv *Advertiser) {
	adv = &Advertiser{cfg: cfg}
	defer func() {
		// The mDNS library touches raw sockets and platform APIs; never let a
		// panic there take the server down.
		if recovered := recover(); recovered != nil {
			log.Printf("[lan-discovery] mDNS advertising disabled: unexpected failure: %v", recovered)
			adv = &Advertiser{cfg: cfg}
		}
	}()

	if cfg.Port <= 0 {
		log.Printf("[lan-discovery] mDNS advertising disabled: invalid port %d", cfg.Port)
		return adv
	}
	ifaces := interfaces()
	if len(ifaces) == 0 {
		log.Printf("[lan-discovery] mDNS advertising disabled: no multicast-capable network interface (Docker users need host networking)")
		return adv
	}
	instance := instanceName(cfg.Name)
	server, err := register(instance, ServiceType, Domain, cfg.Port, cfg.txtRecords(), ifaces)
	if err != nil {
		log.Printf("[lan-discovery] mDNS advertising disabled: %v (Docker users need host networking)", err)
		return adv
	}
	if server == nil {
		log.Printf("[lan-discovery] mDNS advertising disabled: registration unavailable")
		return adv
	}
	adv.server = server
	log.Printf("[lan-discovery] advertising %q as %s.%s on port %d", instance, ServiceType, Domain, cfg.Port)
	return adv
}

// Active reports whether the service is currently advertised.
func (a *Advertiser) Active() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.server != nil
}

// SetServerID updates the serverId TXT record (e.g. once the remote-access
// identity becomes available after start-up).
func (a *Advertiser) SetServerID(serverID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	serverID = strings.ToLower(strings.TrimSpace(serverID))
	if serverID == a.cfg.ServerID {
		return
	}
	a.cfg.ServerID = serverID
	if a.server != nil {
		a.server.SetText(a.cfg.txtRecords())
	}
}

// Shutdown withdraws the advertisement (sends DNS-SD goodbye packets).
func (a *Advertiser) Shutdown() {
	if a == nil {
		return
	}
	a.mu.Lock()
	server := a.server
	a.server = nil
	a.mu.Unlock()
	if server != nil {
		server.Shutdown()
	}
}

// txtRecords returns the TXT key=value pairs for this configuration.
func (c Config) txtRecords() []string {
	records := []string{"txtvers=" + TXTVersion}
	if v := strings.TrimSpace(c.Version); v != "" {
		records = append(records, "version="+v)
	}
	records = append(records, "name="+truncate(displayName(c.Name), 200))
	records = append(records, "apiPath="+strings.TrimRight(strings.TrimSpace(c.BasePath), "/")+"/api")
	if id := strings.TrimSpace(c.ServerID); id != "" {
		records = append(records, "serverId="+id)
	}
	return records
}

func displayName(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "mediastorm"
	}
	host = strings.TrimSuffix(strings.TrimSpace(host), ".local")
	return host
}

func instanceName(name string) string {
	instance := displayName(name)
	// DNS-SD instance labels are limited to 63 octets.
	return truncate(instance, maxInstanceNameLen)
}

func truncate(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := value[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// usableInterfaces returns up, multicast-capable, non-loopback interfaces
// that carry at least one address. Filtering avoids per-packet warnings from
// the mDNS library on interfaces it cannot use.
func usableInterfaces() []net.Interface {
	all, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var result []net.Interface
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagPointToPoint != 0 {
			continue // VPN tunnels (utun, tailscale) do not carry link-local multicast
		}
		addrs, err := iface.Addrs()
		if err != nil || len(addrs) == 0 {
			continue
		}
		result = append(result, iface)
	}
	return result
}
