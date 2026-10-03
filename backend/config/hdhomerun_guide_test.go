package config

import (
	"strings"
	"testing"
)

func TestNormalizeHDHomeRunGuideAuth(t *testing.T) {
	email, ids, err := NormalizeHDHomeRunGuideAuth(" viewer+guide@example.com ", "10bfffff, 10AFFFFF,10bfffff")
	if err != nil || email != "viewer+guide@example.com" || ids != "10AFFFFF,10BFFFFF" {
		t.Fatalf("normalization = %q %q %v", email, ids, err)
	}
	if email, ids, err := NormalizeHDHomeRunGuideAuth(" ", ""); err != nil || email != "" || ids != "" {
		t.Fatal("empty account settings should use automatic authentication")
	}
	for _, test := range []struct{ email, ids string }{
		{"viewer@example.com", ""}, {"", "10AFFFFF"}, {"not-an-email", "10AFFFFF"},
		{"Viewer <viewer@example.com>", "10AFFFFF"}, {"viewer@example.com", "private-id"},
		{"viewer@example.com", "10AFFFFF,"}, {"viewer@example.com", "10AFFFFG"},
		{"viewer@example.com", "10AFFFF"},
	} {
		if _, _, err := NormalizeHDHomeRunGuideAuth(test.email, test.ids); err == nil {
			t.Errorf("invalid account settings accepted: %+v", test)
		} else if strings.Contains(err.Error(), "viewer@example.com") || strings.Contains(err.Error(), "private-id") {
			t.Fatal("validation error exposes account identifiers")
		}
	}
}
