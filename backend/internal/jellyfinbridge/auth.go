package jellyfinbridge

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"novastream/internal/auth"
	"novastream/models"
)

// clientInfo is what Jellyfin clients describe about themselves in the
// MediaBrowser authorization header.
type clientInfo struct {
	Client   string
	Device   string
	DeviceID string
	Version  string
	Token    string
}

// parseAuthorization parses `MediaBrowser Client="x", Device="y", Token="z"`.
// Values may be quoted and URL-encoded (the Kotlin SDK encodes them).
func parseAuthorization(header string) (clientInfo, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return clientInfo{}, false
	}
	scheme, rest, found := strings.Cut(header, " ")
	if !found {
		return clientInfo{}, false
	}
	switch strings.ToLower(scheme) {
	case "mediabrowser", "emby":
	default:
		return clientInfo{}, false
	}
	var info clientInfo
	for _, part := range splitAuthParams(rest) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		switch strings.ToLower(key) {
		case "client":
			info.Client = value
		case "device":
			info.Device = value
		case "deviceid":
			info.DeviceID = value
		case "version":
			info.Version = value
		case "token":
			info.Token = value
		}
	}
	return info, true
}

// splitAuthParams splits on commas that are not inside quotes.
func splitAuthParams(s string) []string {
	var parts []string
	var b strings.Builder
	inQuotes := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			b.WriteRune(r)
		case r == ',' && !inQuotes:
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

// requestClientInfo collects client identity and the access token from every
// place Jellyfin clients put them.
func requestClientInfo(r *http.Request) clientInfo {
	info, _ := parseAuthorization(r.Header.Get("Authorization"))
	if alt, ok := parseAuthorization(r.Header.Get("X-Emby-Authorization")); ok {
		if info.Client == "" {
			info.Client = alt.Client
		}
		if info.Device == "" {
			info.Device = alt.Device
		}
		if info.DeviceID == "" {
			info.DeviceID = alt.DeviceID
		}
		if info.Version == "" {
			info.Version = alt.Version
		}
		if info.Token == "" {
			info.Token = alt.Token
		}
	}
	if info.Token == "" {
		info.Token = strings.TrimSpace(r.Header.Get("X-Emby-Token"))
	}
	if info.Token == "" {
		info.Token = strings.TrimSpace(r.Header.Get("X-MediaBrowser-Token"))
	}
	if info.Token == "" {
		q := r.URL.Query()
		for _, key := range []string{"ApiKey", "api_key", "apiKey", "apikey", "token"} {
			if v := strings.TrimSpace(q.Get(key)); v != "" {
				info.Token = v
				break
			}
		}
	}
	if info.DeviceID == "" {
		info.DeviceID = strings.TrimSpace(r.URL.Query().Get("deviceId"))
	}
	return info
}

// principal is the authenticated caller of a bridge request.
type principal struct {
	Session models.Session
	Profile models.User
	Client  clientInfo
}

type principalKey struct{}

func withPrincipal(r *http.Request, p *principal) *http.Request {
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	// Mirror the main API's auth context so mediastorm handlers invoked
	// in-process see an authenticated account.
	ctx = context.WithValue(ctx, auth.ContextKeyAccountID, p.Session.AccountID)
	ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, p.Session.IsMaster)
	ctx = context.WithValue(ctx, auth.ContextKeySession, p.Session)
	return r.WithContext(ctx)
}

func principalFrom(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}

// authenticate resolves the request's token into a principal. Only
// bridge-scoped sessions are accepted: a normal mediastorm token is not a
// Jellyfin token, and a bridge token never works on the main API.
func (s *Server) authenticate(r *http.Request) (*principal, bool) {
	info := requestClientInfo(r)
	if info.Token == "" || s.deps.Sessions == nil {
		return nil, false
	}
	session, err := s.deps.Sessions.ValidateScoped(info.Token, models.SessionScopeJellyfin)
	if err != nil {
		return nil, false
	}
	if s.deps.Accounts != nil && s.deps.Accounts.IsExpired(session.AccountID) {
		return nil, false
	}
	profile, ok := s.deps.Users.Get(session.ScopeResource)
	if !ok || !s.deps.Users.BelongsToAccount(profile.ID, session.AccountID) {
		return nil, false
	}
	return &principal{Session: session, Profile: profile, Client: info}, true
}
