package pool

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/javi11/nntppool/v2/pkg/nntpcli"
)

// nntppool's provider health check pings with DATE. RFC 3977 servers answer
// 111; upstream v2.3.2 required 200, so every provider was marked offline on
// each health-check cycle. Guards the patched module in third_party/nntppool.
func TestNNTPPingAcceptsRFC3977DateResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("200 test server ready\r\n"))
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.EqualFold(strings.TrimSpace(line), "DATE") {
				_, _ = conn.Write([]byte("111 20261013163919\r\n"))
			} else {
				_, _ = conn.Write([]byte("500 unknown command\r\n"))
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	port := ln.Addr().(*net.TCPAddr).Port
	conn, err := nntpcli.New().Dial(ctx, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := conn.Ping(); err != nil {
		t.Fatalf("Ping() rejected a valid DATE response: %v", err)
	}
}
