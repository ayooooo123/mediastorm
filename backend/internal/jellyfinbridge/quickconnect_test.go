package jellyfinbridge

import (
	"net/http"
	"testing"

	"novastream/config"
	"novastream/internal/jellyfinbridge/dto"
)

func TestQuickConnectFlow(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{QuickConnect: true})
	if enabled := decode[bool](t, h.do(http.MethodGet, "/QuickConnect/Enabled", "", nil)); !enabled {
		t.Fatal("quick connect not reported enabled")
	}

	// The TV starts a request without credentials.
	start := decode[dto.QuickConnectResult](t, h.doWithClient(http.MethodPost, "/QuickConnect/Initiate", "", "Android TV", nil))
	if len(start.Code) != 6 || len(start.Secret) != 64 || start.Authenticated || start.AppName != "Android TV" {
		t.Fatalf("initiate = %+v", start)
	}
	poll := func() *http.Response {
		return h.do(http.MethodGet, "/QuickConnect/Connect?secret="+start.Secret, "", nil).Result()
	}
	if rec := h.do(http.MethodPost, "/Users/AuthenticateWithQuickConnect", "", map[string]string{"Secret": start.Secret}); rec.Code != http.StatusNotFound {
		t.Fatalf("unapproved secret collected: %d", rec.Code)
	}

	// Another profile can only approve as itself.
	tok := h.login("Living Room", "alice-pass")
	other := h.login("bob/Kids", "bob-pass")
	otherID := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", other, nil)).ID
	myID := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", tok, nil)).ID
	if rec := h.do(http.MethodPost, "/QuickConnect/Authorize?code="+start.Code+"&userId="+otherID, tok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("approved for another user: %d", rec.Code)
	}
	if rec := h.do(http.MethodPost, "/QuickConnect/Authorize?Code=000000", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", rec.Code)
	}
	if ok := decode[bool](t, h.do(http.MethodPost, "/QuickConnect/Authorize?Code="+start.Code, tok, nil)); !ok {
		t.Fatal("authorize returned false")
	}
	if rec := h.do(http.MethodPost, "/QuickConnect/Authorize?code="+start.Code, tok, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
		t.Fatalf("second approval: %d", rec.Code)
	}

	status := decode[dto.QuickConnectResult](t, h.do(http.MethodGet, "/QuickConnect/Connect?secret="+start.Secret, "", nil))
	if !status.Authenticated {
		t.Fatalf("status after approval = %+v", status)
	}
	auth := decode[dto.AuthenticationResult](t, h.do(http.MethodPost, "/Users/AuthenticateWithQuickConnect", "", map[string]string{"Secret": start.Secret}))
	if auth.AccessToken == "" || auth.User.ID != myID {
		t.Fatalf("quick connect auth = %+v", auth)
	}
	if me := decode[dto.UserDto](t, h.do(http.MethodGet, "/Users/Me", auth.AccessToken, nil)); me.ID != myID {
		t.Fatalf("quick connect token user = %+v", me)
	}
	// The secret works once.
	if rec := h.do(http.MethodPost, "/Users/AuthenticateWithQuickConnect", "", map[string]string{"Secret": start.Secret}); rec.Code != http.StatusNotFound {
		t.Fatalf("secret reused: %d", rec.Code)
	}
	if resp := poll(); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("poll after collect = %d", resp.StatusCode)
	}
}

func TestQuickConnectDisabledAndLimited(t *testing.T) {
	h := newHarness(t, config.JellyfinBridgeSettings{})
	if enabled := decode[bool](t, h.do(http.MethodGet, "/QuickConnect/Enabled", "", nil)); enabled {
		t.Fatal("quick connect enabled without the setting")
	}
	if rec := h.doWithClient(http.MethodPost, "/QuickConnect/Initiate", "", "Android TV", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("initiate while disabled = %d", rec.Code)
	}

	h = newHarness(t, config.JellyfinBridgeSettings{QuickConnect: true})
	for i := 0; i < quickConnectMaxPerIP; i++ {
		if rec := h.doWithClient(http.MethodPost, "/QuickConnect/Initiate", "", "Android TV", nil); rec.Code != http.StatusOK {
			t.Fatalf("initiate %d = %d", i, rec.Code)
		}
	}
	if rec := h.doWithClient(http.MethodPost, "/QuickConnect/Initiate", "", "Android TV", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("pending requests per address not capped: %d", rec.Code)
	}
}
