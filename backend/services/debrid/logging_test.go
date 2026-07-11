package debrid

import (
	"strings"
	"testing"
)

func TestSafeURLForLogRemovesCredentials(t *testing.T) {
	raw := "https://indexer.example/download?apikey=secret&token=signed#fragment"
	got := safeURLForLog(raw)
	if strings.Contains(got, "secret") || strings.Contains(got, "signed") || strings.Contains(got, "fragment") {
		t.Fatalf("safeURLForLog exposed credentials: %q", got)
	}
	if got != "https://indexer.example/download" {
		t.Fatalf("safeURLForLog=%q", got)
	}
}

func TestSafeURLForLogKeepsInternalReference(t *testing.T) {
	if got := safeURLForLog("12345:7"); got != "12345:7" {
		t.Fatalf("safeURLForLog internal reference=%q", got)
	}
}
