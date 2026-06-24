package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"novastream/internal/auth"
	"novastream/models"
)

type fakeShareSessions struct {
	lastScope    string
	lastDuration time.Duration
	lastAccount  string
}

func (f *fakeShareSessions) CreateScoped(accountID string, isMaster bool, userAgent, ipAddress string, duration time.Duration, scope string) (models.Session, error) {
	f.lastScope = scope
	f.lastDuration = duration
	f.lastAccount = accountID
	return models.Session{
		Token:     "minted-token",
		AccountID: accountID,
		IsMaster:  isMaster,
		Scope:     scope,
		ExpiresAt: time.Now().Add(duration),
	}, nil
}

// fakeShareProfiles satisfies ShareProfileService. The zero value used by
// newTestShareHandler resolves any profile, grants the share flag, and treats
// it as owned by the caller's account — the happy path.
type fakeShareProfiles struct {
	notFound bool // Get returns ok=false
	deny     bool // returned profile has AllowShareLinks=false
	notOwned bool // BelongsToAccount returns false
}

func (f fakeShareProfiles) Get(id string) (models.User, bool) {
	if f.notFound {
		return models.User{}, false
	}
	return models.User{ID: id, AllowShareLinks: !f.deny}, true
}

func (f fakeShareProfiles) BelongsToAccount(profileID, accountID string) bool {
	return !f.notOwned
}

func newTestShareHandler() (*ShareHandler, *fakeShareSessions) {
	sessions := &fakeShareSessions{}
	return NewShareHandler(NewShareStore(nil), sessions, fakeShareProfiles{}, ""), sessions
}

func TestShareCreateDeniedWithoutProfilePermission(t *testing.T) {
	sessions := &fakeShareSessions{}
	h := NewShareHandler(NewShareStore(nil), sessions, fakeShareProfiles{deny: true}, "")

	body, _ := json.Marshal(map[string]string{"sourcePath": "/movies/a.mkv", "profileId": "p1"})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestShareCreateDeniedWhenProfileNotOwned(t *testing.T) {
	sessions := &fakeShareSessions{}
	// Flag is on, but the profile belongs to a different account and the caller
	// is not master.
	h := NewShareHandler(NewShareStore(nil), sessions, fakeShareProfiles{notOwned: true}, "")

	body, _ := json.Marshal(map[string]string{"sourcePath": "/movies/a.mkv", "profileId": "p1"})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestShareCreateDeniedWithoutProfileId(t *testing.T) {
	h, _ := newTestShareHandler() // permissive profile service

	body, _ := json.Marshal(map[string]string{"sourcePath": "/movies/a.mkv"})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestShareCreateStoresWhitelistedParams(t *testing.T) {
	h, _ := newTestShareHandler()

	body, _ := json.Marshal(map[string]string{
		"sourcePath":            "/movies/a.mkv",
		"preselectedAudioTrack": "2",
		"title":                 "A Movie",
		"profileId":             "p1",
		"notAllowedKey":         "should-be-dropped",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp shareLinkView
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" || !strings.HasSuffix(resp.URL, "/share/"+resp.Token) {
		t.Fatalf("unexpected response: %+v", resp)
	}

	stored, ok := h.store.Consume(context.Background(), resp.Token)
	if !ok {
		t.Fatal("share should have been stored")
	}
	if stored.Params["sourcePath"] != "/movies/a.mkv" || stored.Params["preselectedAudioTrack"] != "2" {
		t.Fatalf("whitelisted params not stored: %v", stored.Params)
	}
	if _, exists := stored.Params["notAllowedKey"]; exists {
		t.Fatal("non-whitelisted param should have been dropped")
	}
}

func TestShareCreateRejectsNoSource(t *testing.T) {
	h, _ := newTestShareHandler()
	body, _ := json.Marshal(map[string]string{"title": "No Source"})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestShareCreateRequiresAuth(t *testing.T) {
	h, _ := newTestShareHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(`{"sourcePath":"/x"}`))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestShareOpenMintsScopedSessionAndIsSingleUse(t *testing.T) {
	h, sessions := newTestShareHandler()
	rec, _ := h.store.Create(context.Background(), "acct9", false, map[string]string{
		"sourcePath":            "/movies/a.mkv",
		"preselectedAudioTrack": "1",
	}, time.Hour, 1, "")

	req := httptest.NewRequest(http.MethodGet, "/share/"+rec.Token, nil)
	w := httptest.NewRecorder()
	h.Open(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body: %s)", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if !strings.HasSuffix(parsed.Path, "/watch/playback.html") {
		t.Fatalf("Location path = %q, want /watch/playback.html", parsed.Path)
	}
	q := parsed.Query()
	if q.Get("token") != "minted-token" {
		t.Fatalf("token = %q, want minted-token", q.Get("token"))
	}
	if q.Get("shareMode") != "1" {
		t.Fatalf("shareMode = %q, want 1", q.Get("shareMode"))
	}
	if q.Get("sourcePath") != "/movies/a.mkv" || q.Get("preselectedAudioTrack") != "1" {
		t.Fatalf("captured params missing from redirect: %v", q)
	}
	if sessions.lastScope != models.SessionScopeStream {
		t.Fatalf("minted scope = %q, want %q", sessions.lastScope, models.SessionScopeStream)
	}
	if sessions.lastDuration != SharePlaybackSessionTTL {
		t.Fatalf("minted duration = %v, want %v", sessions.lastDuration, SharePlaybackSessionTTL)
	}
	if sessions.lastAccount != "acct9" {
		t.Fatalf("minted account = %q, want acct9", sessions.lastAccount)
	}

	// Second open is rejected (single use).
	w2 := httptest.NewRecorder()
	h.Open(w2, httptest.NewRequest(http.MethodGet, "/share/"+rec.Token, nil))
	if w2.Code != http.StatusGone {
		t.Fatalf("second open status = %d, want 410", w2.Code)
	}
}

func TestShareCreateHonorsTTLAndMaxUses(t *testing.T) {
	h, _ := newTestShareHandler()

	body, _ := json.Marshal(shareCreateRequest{
		Params:  map[string]string{"sourcePath": "/movies/a.mkv", "profileId": "p1"},
		TTLDays: 7,
		MaxUses: 2,
		Label:   "Family movie night",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/share/create", strings.NewReader(string(body)))
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()

	h.Create(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp shareLinkView
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.MaxUses != 2 || resp.Label != "Family movie night" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	// Two opens succeed, third is gone (max uses = 2).
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.Open(w, httptest.NewRequest(http.MethodGet, "/share/"+resp.Token, nil))
		if w.Code != http.StatusSeeOther {
			t.Fatalf("open %d status = %d, want 303", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.Open(w, httptest.NewRequest(http.MethodGet, "/share/"+resp.Token, nil))
	if w.Code != http.StatusGone {
		t.Fatalf("third open status = %d, want 410", w.Code)
	}
}

func TestShareListAndDelete(t *testing.T) {
	h, _ := newTestShareHandler()
	ctx := context.Background()
	link, _ := h.store.Create(ctx, "acct1", false, map[string]string{"sourcePath": "/a.mkv", "title": "A"}, time.Hour, 0, "")

	req := httptest.NewRequest(http.MethodGet, "/admin/api/share/links", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ContextKeyAccountID, "acct1"))
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("List status = %d", rec.Code)
	}
	var listResp struct {
		Links []shareLinkView `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Links) != 1 || listResp.Links[0].Title != "A" {
		t.Fatalf("unexpected list: %+v", listResp.Links)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/admin/api/share/links?token="+link.Token, nil)
	delReq = delReq.WithContext(context.WithValue(delReq.Context(), auth.ContextKeyAccountID, "acct1"))
	delRec := httptest.NewRecorder()
	h.Delete(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("Delete status = %d (body: %s)", delRec.Code, delRec.Body.String())
	}
	if links, _ := h.store.List(ctx, "acct1", false); len(links) != 0 {
		t.Fatalf("link should be deleted, got %d", len(links))
	}
}

func TestShareOpenUnknownTokenIsGone(t *testing.T) {
	h, _ := newTestShareHandler()
	req := httptest.NewRequest(http.MethodGet, "/share/nope", nil)
	w := httptest.NewRecorder()
	h.Open(w, req)
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", w.Code)
	}
}
