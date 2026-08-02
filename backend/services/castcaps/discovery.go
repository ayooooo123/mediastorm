package castcaps

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Device is a Cast receiver found on the local network.
type Device struct {
	Host         string `json:"host"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	BuildVersion string `json:"buildVersion"`
	UUID         string `json:"uuid"`
	Idle         bool   `json:"idle"`
}

// ID is the cache key. The UUID survives DHCP churn; the host is the fallback
// for receivers that do not report one.
func (d Device) ID() string {
	if strings.TrimSpace(d.UUID) != "" {
		return d.UUID
	}
	return d.Host
}

// eurekaInfo is the subset of /setup/eureka_info worth keeping.
type eurekaInfo struct {
	Name              string `json:"name"`
	SSDPUDN           string `json:"ssdp_udn"`
	CastBuildRevision string `json:"cast_build_revision"`
	BuildVersion      string `json:"build_version"`
	Detail            struct {
		ModelName string `json:"model_name"`
	} `json:"detail"`
	Settings struct {
		Name string `json:"name"`
	} `json:"settings"`
}

// Describe reads a receiver's identity over its setup endpoint. Cast devices
// serve it on 8008 (plain) and 8443 (self-signed TLS); try the cheap one first.
func Describe(ctx context.Context, host string) (Device, error) {
	device := Device{Host: host}
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: insecureTLSConfig(),
		},
	}
	var lastErr error
	for _, url := range []string{
		fmt.Sprintf("http://%s:8008/setup/eureka_info", host),
		fmt.Sprintf("https://%s:8443/setup/eureka_info", host),
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var info eurekaInfo
		err = json.NewDecoder(resp.Body).Decode(&info)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		device.Name = firstNonEmpty(info.Name, info.Settings.Name, host)
		device.Model = info.Detail.ModelName
		device.BuildVersion = firstNonEmpty(info.CastBuildRevision, info.BuildVersion)
		device.UUID = info.SSDPUDN
		return device, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no setup endpoint answered")
	}
	return device, lastErr
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// Discover scans a /24 for open Cast ports and describes what answers. mDNS is
// filtered on plenty of home networks (and inside containers), so a bounded
// port sweep is the dependable way to find receivers.
func Discover(ctx context.Context, cidr string) []Device {
	hosts, err := hostsInCIDR(cidr)
	if err != nil {
		return nil
	}

	var (
		mu      sync.Mutex
		devices []Device
		wg      sync.WaitGroup
	)
	limit := make(chan struct{}, 64)
	for _, host := range hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()

			dialer := net.Dialer{Timeout: 700 * time.Millisecond}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(castPort)))
			if err != nil {
				return
			}
			_ = conn.Close()

			device, err := Describe(ctx, host)
			if err != nil {
				return
			}
			mu.Lock()
			devices = append(devices, device)
			mu.Unlock()
		}(host)
	}
	wg.Wait()
	return devices
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
