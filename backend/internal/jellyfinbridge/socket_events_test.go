package jellyfinbridge

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

// openSocket connects a test WebSocket and consumes the initial keep-alive.
func openSocket(t *testing.T, srv *httptest.Server, token string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /socket?api_key="+url.QueryEscape(token)+" HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(conn)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake: %v", err)
	}
	if _, payload, err := readFrame(br); err != nil || !strings.Contains(string(payload), "ForceKeepAlive") {
		t.Fatalf("first frame: %s %v", payload, err)
	}
	return conn, br
}

func TestSocketPushesUserDataChanges(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	tok := h.login("Living Room", "alice-pass")
	other := h.login("bob/Kids", "bob-pass")
	srv := httptest.NewServer(h.handler)
	defer srv.Close()
	_, mine := openSocket(t, srv, tok)
	otherConn, theirs := openSocket(t, srv, other)

	item := movieID(t, h, tok)
	if rec := h.do(http.MethodPost, "/UserPlayedItems/"+item, tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("mark played = %d", rec.Code)
	}
	op, payload, err := readFrame(mine)
	if err != nil || op != 0x1 {
		t.Fatalf("event frame: %v", err)
	}
	var msg struct {
		MessageType string
		Data        struct {
			UserID       string `json:"UserId"`
			UserDataList []struct {
				ItemID string `json:"ItemId"`
				Played bool
			}
		}
	}
	if err := json.Unmarshal(payload, &msg); err != nil || msg.MessageType != "UserDataChanged" ||
		len(msg.Data.UserDataList) != 1 || msg.Data.UserDataList[0].ItemID != item || msg.Data.UserID == "" {
		t.Fatalf("event = %s", payload)
	}
	// Another profile's socket hears nothing.
	_ = otherConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, payload, err := readFrame(theirs); err == nil {
		t.Fatalf("other profile got %s", payload)
	}
}
