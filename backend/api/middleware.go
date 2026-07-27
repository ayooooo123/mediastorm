package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"novastream/internal/auth"
	"novastream/models"
	"novastream/services/accounts"
	"novastream/services/sessions"
	"novastream/services/users"
)

// streamScopedSourcePaths are source-bearing endpoints a stream-scoped session
// may call only when the request source matches the resource captured at
// share-link creation time.
var streamScopedSourcePaths = map[string]string{
	"/api/video/metadata":         "path",
	"/api/video/direct-url":       "path",
	"/api/video/cropdetect":       "path",
	"/api/video/thumbnails/start": "path",
	"/api/video/credits/detect":   "path",
	"/api/video/hls/start":        "path",
	"/api/video/subtitles/tracks": "path",
	"/api/video/subtitles/start":  "path",
	"/api/live/hls/start":         "url",
	"/api/live/stream":            "url",
}

// queryTokenMediaPaths are byte-stream/media endpoints consumed by browser or
// native media APIs that cannot reliably attach an Authorization header.
var queryTokenMediaPaths = map[string]struct{}{
	"/api/metadata/trailers/proxy":          {},
	"/api/metadata/trailers/prequeue/serve": {},
	"/api/subtitles/download":               {},
	"/api/subtitles/translate":              {},
}

// isStreamScopedRequestAllowed reports whether a stream-scoped share session may
// access this request. Source-bearing routes must match the captured resource.
func isStreamScopedRequestAllowed(r *http.Request, session models.Session) bool {
	path := r.URL.Path
	if isStreamScopedSessionPath(path) {
		return true
	}
	if path == "/api/video/share-progress" {
		return true
	}
	if path == "/api/video/stream" || strings.HasPrefix(path, "/api/video/stream/") {
		return streamScopedSourceMatches(session.ScopeResource, r.URL.Query().Get("path"))
	}
	if queryParam, ok := streamScopedSourcePaths[path]; ok {
		return streamScopedSourceMatches(session.ScopeResource, r.URL.Query().Get(queryParam))
	}
	return false
}

func isStreamScopedSessionPath(path string) bool {
	if strings.HasPrefix(path, "/api/video/hls/") {
		return path != "/api/video/hls/start"
	}
	return strings.HasPrefix(path, "/api/video/subtitles/") &&
		path != "/api/video/subtitles/tracks" &&
		path != "/api/video/subtitles/start"
}

func streamScopedSourceMatches(scopeResource, requested string) bool {
	scopeResource = normalizeStreamScopedSource(scopeResource)
	requested = normalizeStreamScopedSource(requested)
	if requested == models.SessionScopeResourcePlaceholder {
		return scopeResource != ""
	}
	return scopeResource != "" && requested != "" && scopeResource == requested
}

func normalizeStreamScopedSource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "webdav/") {
		value = "/" + strings.TrimPrefix(value, "webdav/")
	} else if strings.HasPrefix(value, "/webdav/") {
		value = strings.TrimPrefix(value, "/webdav")
	}
	return value
}

// Re-export from auth package for backward compatibility
var (
	GetAccountID = auth.GetAccountID
	IsMaster     = auth.IsMaster
)

// AccountAuthMiddleware creates middleware that validates session tokens.
// Tokens can be provided via Authorization header or ?token= query param.
// If accountsSvc is provided, expired accounts will be rejected.
func AccountAuthMiddleware(sessionsSvc *sessions.Service, accountsSvc *accounts.Service) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always allow OPTIONS for CORS
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Extract token from header or query param
			token := extractToken(r)
			if token == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
				return
			}

			if sessionsSvc == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"error": "session service unavailable"})
				return
			}

			session, err := sessionsSvc.Validate(token)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid or expired session"})
				return
			}

			// Check if the account has expired
			if accountsSvc != nil && accountsSvc.IsExpired(session.AccountID) {
				sessionsSvc.RevokeAllForAccount(session.AccountID)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error": "account has expired"})
				return
			}

			// Stream-scoped sessions (one-time share links) may only reach
			// streaming/playback endpoints — never the rest of the account API.
			if session.Scope == models.SessionScopeStream && !isStreamScopedRequestAllowed(r, session) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "session not permitted for this endpoint"})
				return
			}

			// Valid session - inject account context
			ctx := context.WithValue(r.Context(), auth.ContextKeyAccountID, session.AccountID)
			ctx = context.WithValue(ctx, auth.ContextKeyIsMaster, session.IsMaster)
			ctx = context.WithValue(ctx, auth.ContextKeySession, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// MasterOnlyMiddleware creates middleware that only allows master accounts.
func MasterOnlyMiddleware() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			if !IsMaster(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"error": "master account required"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// ProfileOwnershipMiddleware creates middleware that verifies profile ownership.
// Master accounts can access any profile; regular accounts can only access their own.
func ProfileOwnershipMiddleware(usersSvc *users.Service) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Master accounts bypass ownership check
			if IsMaster(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Get profile ID from URL
			vars := mux.Vars(r)
			profileID := vars["userID"]
			if profileID == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Check ownership
			accountID := GetAccountID(r)
			if !usersSvc.BelongsToAccount(profileID, accountID) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": "profile not found"})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractToken extracts the session token from headers or query param.
// Priority: Authorization header > X-PIN header > ?token= query param
func extractToken(r *http.Request) string {
	// First try Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			if token := strings.TrimSpace(parts[1]); token != "" {
				return token
			}
		}
	}

	// Try X-PIN header (for reverse proxy compatibility, e.g., Traefik)
	if token := strings.TrimSpace(r.Header.Get("X-PIN")); token != "" {
		return token
	}

	// Fall back to a query parameter only for player/media URLs whose consumers
	// cannot attach headers. General API bearer tokens must never be put in URLs.
	if queryTokenAllowed(r.URL.Path) {
		if token := strings.TrimSpace(r.URL.Query().Get("token")); token != "" {
			return token
		}
	}

	return ""
}

func queryTokenAllowed(path string) bool {
	if path == "/api/video/stream" || strings.HasPrefix(path, "/api/video/stream/") ||
		path == "/api/video/share-progress" || isStreamScopedSessionPath(path) {
		return true
	}
	if _, ok := streamScopedSourcePaths[path]; ok {
		return true
	}
	if _, ok := queryTokenMediaPaths[path]; ok {
		return true
	}
	if strings.HasPrefix(path, "/api/live/hls/") || path == "/api/live/stream" {
		return true
	}
	if strings.HasPrefix(path, "/api/live/recordings/") && strings.HasSuffix(path, "/stream") {
		return true
	}
	// Native image renderers cannot attach the API Authorization header. These
	// endpoints return image bytes only, so allow the same query-token flow used
	// by video and HLS media URLs without broadening general API access.
	if strings.HasPrefix(path, "/api/library/items/") && strings.Contains(path, "/artwork/") {
		return true
	}
	return false
}
