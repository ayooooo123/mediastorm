package jellyfinbridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novastream/config"
)

func TestClientLogIsStored(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	h.srv.deps.CacheDir = t.TempDir()
	req := httptest.NewRequest(http.MethodPost, "/ClientLog/Document", strings.NewReader("java.lang.NullPointerException: boom\n\tat org.jellyfin.Foo"))
	req.Header.Set("Authorization", `MediaBrowser Client="Jellyfin Android TV", Device="Fire TV", DeviceId="ftv", Version="0.19.10"`)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "FileName") {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	files, _ := filepath.Glob(filepath.Join(h.srv.clientLogDir(), "*.log"))
	if len(files) != 1 || !strings.Contains(files[0], "Jellyfin-Android-TV_0.19.10") {
		t.Fatalf("stored files = %v", files)
	}
	body, _ := os.ReadFile(files[0])
	if !strings.HasPrefix(string(body), "java.lang.NullPointerException") {
		t.Fatalf("stored body = %q", body)
	}
}
