package castcaps

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// dialProbeTimeout bounds the TCP connect used to decide whether a LAN address
// is worth an HTTP request at all.
const dialProbeTimeout = 700 * time.Millisecond

// discoveryConcurrency bounds the sweep so a /24 finishes quickly without
// flooding a home router's connection table.
const discoveryConcurrency = 64

// Discover sweeps a /24 for hosts serving the Cast setup port and describes the
// ones that answer. mDNS is filtered on plenty of home networks (and inside
// containers), so a bounded port sweep is the dependable way to find receivers.
//
// Passive throughout: a TCP connect to 8008 and two HTTP GETs. Nothing is
// launched and nothing is played.
func Discover(ctx context.Context, cidr string) []Identity {
	hosts, err := hostsInCIDR(cidr)
	if err != nil {
		return nil
	}

	var (
		mu         sync.Mutex
		identities []Identity
		wg         sync.WaitGroup
	)
	limit := make(chan struct{}, discoveryConcurrency)
	for _, host := range hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()

			dialer := net.Dialer{Timeout: dialProbeTimeout}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(setupPort)))
			if err != nil {
				return
			}
			_ = conn.Close()

			identity, err := Describe(ctx, host)
			if err != nil {
				return
			}
			mu.Lock()
			identities = append(identities, identity)
			mu.Unlock()
		}(host)
	}
	wg.Wait()
	return identities
}

func hostsInCIDR(cidr string) ([]string, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ones, bits := network.Mask.Size()
	if bits-ones > 8 {
		return nil, fmt.Errorf("refusing to scan a network larger than /24: %s", cidr)
	}
	var hosts []string
	for ip := network.IP.Mask(network.Mask); network.Contains(ip); ip = nextIP(ip) {
		last := ip[len(ip)-1]
		if last == 0 || last == 255 {
			continue
		}
		hosts = append(hosts, ip.String())
	}
	return hosts, nil
}

func nextIP(ip net.IP) net.IP {
	next := make(net.IP, len(ip))
	copy(next, ip)
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			break
		}
	}
	return next
}

// LocalCIDR returns the /24 of the first non-loopback IPv4 interface address.
func LocalCIDR() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ipv4 := ipNet.IP.To4()
		if ipv4 == nil {
			continue
		}
		return fmt.Sprintf("%d.%d.%d.0/24", ipv4[0], ipv4[1], ipv4[2])
	}
	return ""
}
