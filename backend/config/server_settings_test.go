package config

import (
	"reflect"
	"testing"
)

func TestNormalizeAllowedPrivateMediaOrigins(t *testing.T) {
	settings := ServerSettings{AllowedPrivateMediaOrigins: []string{
		" http://LOCALHOST:8080/adapters/addon/file.mkv?token=secret#fragment ",
		"http://localhost:8080",
		"https://[fd00::1]:8443/media",
	}}

	if err := settings.NormalizeAllowedPrivateMediaOrigins(); err != nil {
		t.Fatalf("NormalizeAllowedPrivateMediaOrigins() error = %v", err)
	}
	want := []string{"http://localhost:8080", "https://[fd00::1]:8443"}
	if !reflect.DeepEqual(settings.AllowedPrivateMediaOrigins, want) {
		t.Fatalf("allowed origins = %#v, want %#v", settings.AllowedPrivateMediaOrigins, want)
	}
}

func TestNormalizeAllowedPrivateMediaOriginsRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{
		"localhost:8080",
		"file:///etc/passwd",
		"http://user:password@localhost:8080",
	} {
		settings := ServerSettings{AllowedPrivateMediaOrigins: []string{raw}}
		if err := settings.NormalizeAllowedPrivateMediaOrigins(); err == nil {
			t.Errorf("NormalizeAllowedPrivateMediaOrigins(%q) error = nil, want rejection", raw)
		}
	}
}
