package jellyfinbridge

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"

	"novastream/config"
)

func TestAccessLogCoversConnectionRequests(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	h.doWithClient(http.MethodGet, "/System/Info/Public", "", "Jellyfin Android", nil)
	h.doWithClient(http.MethodGet, "/web/index.html", "", "Jellyfin Android", nil)
	tok := h.login("Living Room", "alice-pass")
	buf.Reset()
	h.do(http.MethodGet, "/UserViews", tok, nil) // browsing: not logged
	if strings.Contains(buf.String(), "/UserViews") {
		t.Fatalf("browsing request logged: %s", buf.String())
	}
	h.doWithClient(http.MethodGet, "/System/Info/Public", "", "Jellyfin Android", nil)
	if out := buf.String(); !strings.Contains(out, "/System/Info/Public -> 200") || !strings.Contains(out, `client "Jellyfin Android 1 / dev"`) {
		t.Fatalf("connection request not logged: %s", out)
	}
}
