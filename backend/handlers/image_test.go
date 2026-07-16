package handlers

import "testing"

func TestNormalizeProxyWidth(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  int
	}{
		{name: "4K width", width: 3840, want: 3840},
		{name: "above 4K", width: 3841, want: 3840},
		{name: "unset", width: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeProxyWidth(tt.width); got != tt.want {
				t.Fatalf("normalizeProxyWidth(%d) = %d, want %d", tt.width, got, tt.want)
			}
		})
	}
}
