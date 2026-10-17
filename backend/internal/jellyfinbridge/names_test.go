package jellyfinbridge

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func publicNames(t *testing.T, h *harness) []string {
	t.Helper()
	var names []string
	for _, u := range decode[[]dto.UserDto](t, h.do(http.MethodGet, "/Users/Public", "", nil)) {
		names = append(names, u.Name)
	}
	sort.Strings(names)
	return names
}

func signedInName(t *testing.T, h *harness, username, password string) string {
	t.Helper()
	rec := h.do(http.MethodPost, "/Users/AuthenticateByName", "", map[string]string{"Username": username, "Pw": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %q: %d", username, rec.Code)
	}
	res := decode[dto.AuthenticationResult](t, rec)
	me := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", res.AccessToken, nil))
	if me.Name != res.User.Name {
		t.Fatalf("/Users/Me name %q, sign-in result %q", me.Name, res.User.Name)
	}
	return res.User.Name
}

// Every profile is "account/profile" ("Living Room" exists on alice's and
// bob's accounts); the public list shows those names only when enabled.
func TestProfileNamesWithAccounts(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	if got := publicNames(t, h); len(got) != 0 {
		t.Fatalf("list shown by default: %v", got)
	}
	for _, tc := range []struct{ typed, pw, want string }{
		{"bob/Living Room", "bob-pass", "bob/Living Room"},
		{"alice/Living Room", "alice-pass", "alice/Living Room"},
		{"Kids", "bob-pass", "bob/Kids"},                   // the plain name still signs in
		{"Living Room", "alice-pass", "alice/Living Room"}, // the password decides
	} {
		if got := signedInName(t, h, tc.typed, tc.pw); got != tc.want {
			t.Fatalf("signed in as %q: name %q, want %q", tc.typed, got, tc.want)
		}
	}

	h = newHarness(t, config.JellyfinBridgeSettings{ShowUserList: true})
	for _, want := range []string{"alice/Living Room", "bob/Kids", "bob/Living Room"} {
		if got := publicNames(t, h); !strings.Contains("|"+strings.Join(got, "|")+"|", "|"+want+"|") {
			t.Fatalf("public names = %v, missing %q", got, want)
		}
	}
}

// Items without images still carry ImageTags: Android TV calls containsKey on
// it without a null check.
func TestItemsAlwaysCarryImageTags(t *testing.T) {
	raw, err := json.Marshal(dto.BaseItemDto{ID: "x", Name: "No art"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ImageTags":{}`) || !strings.Contains(string(raw), `"BackdropImageTags":[]`) {
		t.Fatalf("item json = %s", raw)
	}
}

// Version stubs and any other media source carry a valid enum for
// TranscodingSubProtocol; an empty one breaks the Android TV app.
func TestMediaSourceProtocolNeverEmpty(t *testing.T) {
	raw, err := json.Marshal(versionStub("abc", "Auto", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"TranscodingSubProtocol":"http"`) {
		t.Fatalf("stub json = %s", raw)
	}
}
