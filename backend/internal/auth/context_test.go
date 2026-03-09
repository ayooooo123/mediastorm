package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAccountID(t *testing.T) {
	tests := []struct {
		name     string
		ctxVal   any
		expected string
	}{
		{"returns account ID when set", "acct-123", "acct-123"},
		{"returns empty string when not set", nil, ""},
		{"returns empty string on type mismatch", 42, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.ctxVal != nil {
				ctx := context.WithValue(r.Context(), ContextKeyAccountID, tt.ctxVal)
				r = r.WithContext(ctx)
			}
			got := GetAccountID(r)
			if got != tt.expected {
				t.Errorf("GetAccountID() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestIsMaster(t *testing.T) {
	tests := []struct {
		name     string
		ctxVal   any
		expected bool
	}{
		{"returns true when master", true, true},
		{"returns false when not master", false, false},
		{"returns false when not set", nil, false},
		{"returns false on type mismatch", "true", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.ctxVal != nil {
				ctx := context.WithValue(r.Context(), ContextKeyIsMaster, tt.ctxVal)
				r = r.WithContext(ctx)
			}
			got := IsMaster(r)
			if got != tt.expected {
				t.Errorf("IsMaster() = %v, want %v", got, tt.expected)
			}
		})
	}
}
