package auth

import (
	"net/http"

	"novastream/models"
)

// ContextKey is the type used for context keys
type ContextKey string

const (
	// ContextKeyAccountID is the key for the account ID in the context
	ContextKeyAccountID ContextKey = "accountID"
	// ContextKeyIsMaster is the key for the master flag in the context
	ContextKeyIsMaster ContextKey = "isMaster"
	// ContextKeySession is the key for the session in the context
	ContextKeySession ContextKey = "session"
)

// GetAccountID retrieves the authenticated account ID from the request context.
func GetAccountID(r *http.Request) string {
	if id, ok := r.Context().Value(ContextKeyAccountID).(string); ok {
		return id
	}
	return ""
}

// GetSessionScope returns the scope of the authenticated session, or "" when no
// session is present or it is a normal full-access login. A "stream" scope marks
// a request authenticated via a one-time share link (see models.SessionScopeStream).
func GetSessionScope(r *http.Request) string {
	if session, ok := r.Context().Value(ContextKeySession).(models.Session); ok {
		return session.Scope
	}
	return ""
}

// GetSession returns the authenticated session stored by API middleware.
func GetSession(r *http.Request) (models.Session, bool) {
	session, ok := r.Context().Value(ContextKeySession).(models.Session)
	return session, ok
}

// IsShareLinkRequest reports whether the request is authenticated by a share-link
// (stream-scoped) session.
func IsShareLinkRequest(r *http.Request) bool {
	return GetSessionScope(r) == models.SessionScopeStream
}

// IsMaster checks if the authenticated account is a master account.
func IsMaster(r *http.Request) bool {
	if isMaster, ok := r.Context().Value(ContextKeyIsMaster).(bool); ok {
		return isMaster
	}
	return false
}
