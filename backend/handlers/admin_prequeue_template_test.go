package handlers

import (
	"strings"
	"testing"
)

func TestPrequeuePageOffersBadgeFilters(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/prequeue.html")
	if err != nil {
		t.Fatalf("read prequeue template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`id="prequeueBadgeFilter"`,
		`<option value="warm">Warm</option>`,
		`<option value="manual">Manual · Forever</option>`,
		`<option value="status:ready">Ready</option>`,
		`function prequeueMatchesBadgeFilter(entry, badgeFilter)`,
		`prequeueMatchesBadgeFilter(entry, badgeFilter)`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("prequeue template missing badge filter marker %q", marker)
		}
	}
}
