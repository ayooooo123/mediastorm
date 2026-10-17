package jellyfinbridge

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
)

// discoveryResponder answers Jellyfin's UDP server discovery
// ("who is JellyfinServer?" broadcast to port 7359) so clients on the LAN
// list the bridge automatically.
type discoveryResponder struct {
	conn *net.UDPConn
	once sync.Once
}

const discoveryPort = 7359

func startDiscovery(s *Server) (*discoveryResponder, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: discoveryPort})
	if err != nil {
		return nil, err
	}
	d := &discoveryResponder{conn: conn}
	go d.serve(s)
	return d, nil
}

func (d *discoveryResponder) serve(s *Server) {
	buf := make([]byte, 1024)
	for {
		n, addr, err := d.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if !strings.Contains(strings.ToLower(string(buf[:n])), "who is jellyfinserver?") {
			continue
		}
		// Discovery is a LAN feature: never answer (or amplify toward) public
		// addresses, which would also reveal the server's internal IP.
		if !addr.IP.IsPrivate() && !addr.IP.IsLoopback() && !addr.IP.IsLinkLocalUnicast() {
			continue
		}
		reply, _ := json.Marshal(map[string]any{
			"Address":         fmt.Sprintf("http://%s:%d", localIPFor(addr.IP), s.cfg.Port),
			"Id":              s.serverID,
			"Name":            s.settings().ServerName,
			"EndpointAddress": nil,
		})
		if _, err := d.conn.WriteToUDP(reply, addr); err != nil {
			log.Printf("[jellyfin-bridge] discovery reply to %s: %v", addr, err)
		}
	}
}

func (d *discoveryResponder) close() {
	d.once.Do(func() { _ = d.conn.Close() })
}

// localIPFor returns the local address used to reach remote.
func localIPFor(remote net.IP) string {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: remote, Port: discoveryPort})
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
