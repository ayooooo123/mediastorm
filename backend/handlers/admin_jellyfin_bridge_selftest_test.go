package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/config"
)

// fakeBridge answers the requests the self-test makes.
func fakeBridge(t *testing.T, webClient bool, loggedOut *atomic.Bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/System/Info/Public", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ServerName": "mediastorm", "Version": "10.11.11", "Id": "bridge-id"})
	})
	mux.HandleFunc("/web/", func(w http.ResponseWriter, _ *http.Request) {
		if !webClient {
			http.NotFound(w, nil)
			return
		}
		_, _ = w.Write([]byte("<html></html>"))
	})
	mux.HandleFunc("/Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Username, Pw string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Username != "Kids" || in.Pw != "secret" {
			http.Error(w, "Error processing request.", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]string{"Id": "u1", "Name": "household/Kids"}})
	})
	mux.HandleFunc("/UserViews", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), `Token="tok"`) || r.URL.Query().Get("userId") != "u1" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]string{{"Name": "My Movies"}, {"Name": "My Shows"}}})
	})
	mux.HandleFunc("/Sessions/Logout", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), `Token="tok"`) {
			loggedOut.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runBridgeSelfTest(t *testing.T, admin *JellyfinBridgeAdmin, body string) jellyfinBridgeTestResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	admin.Test(rec, httptest.NewRequest(http.MethodPost, "/admin/api/jellyfin-bridge/test", bytes.NewBufferString(body)))
	var got jellyfinBridgeTestResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("test = %d %s", rec.Code, rec.Body.String())
	}
	return got
}

func checkStatuses(checks []jellyfinBridgeCheck) map[string]string {
	out := map[string]string{}
	for _, c := range checks {
		out[c.Name] = c.Status
	}
	return out
}

func newSelfTestAdmin(t *testing.T, running bool, base string) *JellyfinBridgeAdmin {
	t.Helper()
	mgr := config.NewManager(filepath.Join(t.TempDir(), "settings.json"))
	ctrl := &fakeBridgeController{}
	if running {
		ctrl.applied = []config.JellyfinBridgeSettings{{Enabled: true, Port: 8096, ServerID: "bridge-id"}}
	}
	admin := NewJellyfinBridgeAdmin(mgr, ctrl)
	admin.testBaseURL = base
	return admin
}

func TestJellyfinBridgeSelfTestNotRunning(t *testing.T) {
	got := runBridgeSelfTest(t, newSelfTestAdmin(t, false, ""), `{}`)
	if got.OK || len(got.Checks) != 1 || got.Checks[0].Name != "Listener" || got.Checks[0].Status != bridgeCheckFail {
		t.Fatalf("got %+v, want a failed listener check only", got)
	}
}

func TestJellyfinBridgeSelfTestSignsInAndOut(t *testing.T) {
	var loggedOut atomic.Bool
	srv := fakeBridge(t, true, &loggedOut)
	got := runBridgeSelfTest(t, newSelfTestAdmin(t, true, srv.URL), `{"username":"Kids","password":"secret"}`)
	want := map[string]string{"Listener": "ok", "Server info": "ok", "Web client": "ok", "Sign-in": "ok", "Libraries": "ok"}
	if s := checkStatuses(got.Checks); !got.OK || len(s) != len(want) {
		t.Fatalf("checks = %+v", got.Checks)
	} else {
		for name, status := range want {
			if s[name] != status {
				t.Fatalf("%s = %q, want %q (%+v)", name, s[name], status, got.Checks)
			}
		}
	}
	if !strings.Contains(got.Checks[len(got.Checks)-1].Detail, "My Movies, My Shows") {
		t.Fatalf("libraries detail = %q", got.Checks[len(got.Checks)-1].Detail)
	}
	if !loggedOut.Load() {
		t.Fatal("test session was not signed out")
	}
}

func TestJellyfinBridgeSelfTestWithoutCredentials(t *testing.T) {
	var loggedOut atomic.Bool
	srv := fakeBridge(t, false, &loggedOut)
	got := runBridgeSelfTest(t, newSelfTestAdmin(t, true, srv.URL), `{}`)
	s := checkStatuses(got.Checks)
	if !got.OK || s["Web client"] != bridgeCheckWarn || s["Sign-in"] != bridgeCheckSkip {
		t.Fatalf("checks = %+v", got.Checks)
	}
}

func TestJellyfinBridgeSelfTestBadPasswordExplainsUserName(t *testing.T) {
	var loggedOut atomic.Bool
	srv := fakeBridge(t, true, &loggedOut)
	got := runBridgeSelfTest(t, newSelfTestAdmin(t, true, srv.URL), `{"username":"household","password":"secret"}`)
	last := got.Checks[len(got.Checks)-1]
	if got.OK || last.Name != "Sign-in" || last.Status != bridgeCheckFail || !strings.Contains(last.Detail, "account/profile") {
		t.Fatalf("checks = %+v", got.Checks)
	}
	if loggedOut.Load() {
		t.Fatal("signed out a session that never signed in")
	}
}

func TestLoopbackHost(t *testing.T) {
	for in, want := range map[string]string{"": "127.0.0.1", "0.0.0.0": "127.0.0.1", "::": "127.0.0.1", "[::]": "127.0.0.1", "192.168.1.5": "192.168.1.5", "::1": "::1"} {
		if got := loopbackHost(in); got != want {
			t.Errorf("loopbackHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJellyfinBridgeSelfTestChecksAddressFromServer(t *testing.T) {
	var loggedOut atomic.Bool
	srv := fakeBridge(t, true, &loggedOut)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "someone-else"})
	}))
	t.Cleanup(other.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for _, tc := range []struct {
		name, address, want, detail string
	}{
		{"this bridge", srv.URL + "/", bridgeCheckOK, "This bridge answers"},
		{"another server", other.URL, bridgeCheckFail, "different server"},
		{"nothing listening", closed.URL, bridgeCheckFail, "publish the bridge port"},
		{"not a URL", "ftp://host", bridgeCheckFail, "Not a valid address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"address": tc.address})
			got := runBridgeSelfTest(t, newSelfTestAdmin(t, true, srv.URL), string(body))
			var check *jellyfinBridgeCheck
			for i := range got.Checks {
				if strings.HasPrefix(got.Checks[i].Name, "Reachable at ") {
					check = &got.Checks[i]
				}
			}
			if check == nil || check.Status != tc.want || !strings.Contains(check.Detail, tc.detail) {
				t.Fatalf("checks = %+v", got.Checks)
			}
			if check.Name != "Reachable at "+strings.TrimRight(tc.address, "/") {
				t.Fatalf("name = %q", check.Name)
			}
		})
	}
}
