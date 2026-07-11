package slogutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactingWriterRemovesCredentialForms(t *testing.T) {
	var output bytes.Buffer
	writer := NewRedactingWriter(&output)
	input := `GET /download?apikey=secret&x=1 %3Ftoken%3Dencoded%26x%3D1 {"token":"json-secret"} Authorization: Bearer bearer-secret`
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, secret := range []string{"secret", "encoded", "json-secret", "bearer-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted log still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("redacted marker missing: %s", got)
	}
}
