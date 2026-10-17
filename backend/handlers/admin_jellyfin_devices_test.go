package handlers

import "testing"

func TestJellyfinDeviceIDHidesToken(t *testing.T) {
	id := jellyfinDeviceID("secret-token")
	if len(id) != 24 || id == jellyfinDeviceID("other-token") || id != jellyfinDeviceID("secret-token") {
		t.Fatalf("device id = %q", id)
	}
}
