package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Self-test results, one per check, in the order they ran.
const (
	bridgeCheckOK   = "ok"
	bridgeCheckWarn = "warn"
	bridgeCheckFail = "fail"
	bridgeCheckSkip = "skip"
)

type jellyfinBridgeCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type jellyfinBridgeTestResponse struct {
	OK     bool                  `json:"ok"`
	Checks []jellyfinBridgeCheck `json:"checks"`
}

// bridgeTestAuth identifies the self-test to the bridge like a Jellyfin app.
const bridgeTestAuth = `MediaBrowser Client="mediastorm bridge test", Device="mediastorm", DeviceId="mediastorm-bridge-selftest", Version="1.0"`

// Test probes the running bridge over loopback the way a Jellyfin app does:
// server info, the web client and, given a user name and password, sign-in
// and the library list. The test session is signed out again.
func (a *JellyfinBridgeAdmin) Test(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&in)
	}
	s, err := a.cfg.Load()
	if err != nil {
		writeBridgeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st := a.ctrl.Status()
	resp := jellyfinBridgeTestResponse{}
	add := func(name, status, detail string) {
		resp.Checks = append(resp.Checks, jellyfinBridgeCheck{Name: name, Status: status, Detail: detail})
	}
	defer func() {
		resp.OK = true
		for _, c := range resp.Checks {
			if c.Status == bridgeCheckFail {
				resp.OK = false
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}()

	if !st.Running {
		detail := "The bridge is not running. Turn it on and save."
		if s.JellyfinBridge.Enabled && st.Error != "" {
			detail = "The bridge is enabled but could not start: " + st.Error
		}
		add("Listener", bridgeCheckFail, detail)
		return
	}
	add("Listener", bridgeCheckOK, "Listening on port "+strconv.Itoa(st.Port)+".")

	base := a.testBaseURL
	if base == "" {
		base = "http://" + net.JoinHostPort(loopbackHost(s.JellyfinBridge.Host), strconv.Itoa(st.Port))
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}

	var info struct {
		ServerName string
		Version    string
	}
	code, body, err := bridgeRequest(ctx, client, http.MethodGet, base+"/System/Info/Public", "", nil)
	if err != nil || code != http.StatusOK || json.Unmarshal(body, &info) != nil {
		add("Server info", bridgeCheckFail, "No answer from "+base+"/System/Info/Public: "+describeBridgeFailure(code, err))
		return
	}
	add("Server info", bridgeCheckOK, fmt.Sprintf("Answers as \"%s\", Jellyfin %s.", info.ServerName, info.Version))

	if code, _, err := bridgeRequest(ctx, client, http.MethodGet, base+"/web/", "", nil); err == nil && code == http.StatusOK {
		add("Web client", bridgeCheckOK, "jellyfin-web is served at /web.")
	} else {
		add("Web client", bridgeCheckWarn, "Not served ("+describeBridgeFailure(code, err)+"). Native apps work without it; the Jellyfin Android app and Jellyfin Media Player need it.")
	}

	username := strings.TrimSpace(in.Username)
	if username == "" {
		add("Sign-in", bridgeCheckSkip, "Enter a profile name and password to test signing in.")
		return
	}
	payload, _ := json.Marshal(map[string]string{"Username": username, "Pw": in.Password})
	code, body, err = bridgeRequest(ctx, client, http.MethodPost, base+"/Users/AuthenticateByName", "", payload)
	var auth struct {
		AccessToken string
		User        struct{ ID, Name string } `json:"User"`
	}
	if err != nil || code != http.StatusOK || json.Unmarshal(body, &auth) != nil || auth.AccessToken == "" {
		detail := "Sign-in failed: " + describeBridgeFailure(code, err) + "."
		switch code {
		case http.StatusUnauthorized:
			detail += " Sign in with the profile name (e.g. \"Kids\") or account/profile (e.g. \"household/Kids\"), not the account name alone, and the account password."
		case http.StatusTooManyRequests:
			detail += " Too many failed attempts; wait a few minutes."
		}
		add("Sign-in", bridgeCheckFail, detail)
		return
	}
	token := auth.AccessToken
	defer func() {
		// Sign the test session out so it does not linger as a device.
		_, _, _ = bridgeRequest(context.Background(), client, http.MethodPost, base+"/Sessions/Logout", token, nil)
	}()
	add("Sign-in", bridgeCheckOK, "Signed in as \""+auth.User.Name+"\".")

	var views struct {
		Items []struct{ Name string }
	}
	code, body, err = bridgeRequest(ctx, client, http.MethodGet, base+"/UserViews?userId="+auth.User.ID, token, nil)
	if err != nil || code != http.StatusOK || json.Unmarshal(body, &views) != nil {
		add("Libraries", bridgeCheckFail, "Could not list libraries: "+describeBridgeFailure(code, err)+".")
		return
	}
	if len(views.Items) == 0 {
		add("Libraries", bridgeCheckWarn, "Signed in, but the profile has no libraries.")
		return
	}
	names := make([]string, 0, len(views.Items))
	for _, v := range views.Items {
		names = append(names, v.Name)
	}
	add("Libraries", bridgeCheckOK, strconv.Itoa(len(names))+" libraries: "+strings.Join(names, ", ")+".")
}

// loopbackHost is the address the self-test dials: the bind address, or
// loopback when the bridge listens on all interfaces.
func loopbackHost(bind string) string {
	bind = strings.Trim(strings.TrimSpace(bind), "[]")
	if bind == "" || bind == "0.0.0.0" || bind == "::" {
		return "127.0.0.1"
	}
	return bind
}

func bridgeRequest(ctx context.Context, client *http.Client, method, url, token string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	authHeader := bridgeTestAuth
	if token != "" {
		authHeader += `, Token="` + token + `"`
	}
	req.Header.Set("Authorization", authHeader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, data, err
}

func describeBridgeFailure(code int, err error) string {
	if err != nil {
		return err.Error()
	}
	return "HTTP " + strconv.Itoa(code)
}
