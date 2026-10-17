package jellyfinbridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"novastream/config"
)

func TestWebClientServedAtWeb(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"index.html": "<html>jf</html>", "Main.Bundle.js": "js", "config.json": "{}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, config.JellyfinBridgeSettings{WebClientDir: dir})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	if rec := get("/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/web/" {
		t.Fatalf("/ = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("/web/index.html"); rec.Code != http.StatusOK || rec.Body.String() != "<html>jf</html>" {
		t.Fatalf("index = %d %q", rec.Code, rec.Body.String())
	}
	if rec := get("/web/Main.Bundle.js"); rec.Code != http.StatusOK || rec.Body.String() != "js" {
		t.Fatalf("case-sensitive asset = %d", rec.Code)
	}
	if rec := get("/web/missing.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d", rec.Code)
	}
	if rec := get("/web/home"); rec.Code != http.StatusOK || rec.Body.String() != "<html>jf</html>" {
		t.Fatalf("client route = %d", rec.Code)
	}
	if rec := get("/web/../../etc/passwd"); rec.Code == http.StatusOK && rec.Body.String() != "<html>jf</html>" {
		t.Fatal("path traversal")
	}
	if rec := get("/System/Info/Public"); rec.Code != http.StatusOK {
		t.Fatalf("API broken behind the web client: %d", rec.Code)
	}

	// Without a build, /web stays an API 404 instead of a broken page.
	none := newHarness(t, config.JellyfinBridgeSettings{WebClientDir: filepath.Join(dir, "absent")})
	rec := httptest.NewRecorder()
	none.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/index.html", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("served /web without a build")
	}
}
