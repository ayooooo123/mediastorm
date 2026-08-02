package handlers

import (
	"strings"
	"testing"
)

func TestAdminUsersPageExplainsAndRendersAccessHierarchy(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/accounts.html")
	if err != nil {
		t.Fatalf("read accounts template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`Households, People &amp; Devices`,
		`id="tab-households"`,
		`class="hierarchy-guide"`,
		`Household</span><span class="hierarchy-level-tech">Account`,
		`Person</span><span class="hierarchy-level-tech">Profile`,
		`Device</span><span class="hierarchy-level-tech">Client`,
		`fetch(basePath + '/api/clients')`,
		`function renderHouseholds()`,
		`function renderPersonRow(profile, forceShowStale = false)`,
		`function renderDeviceRow(client, orphaned)`,
		`function reassignClient(clientId, newProfileId`,
		`function pingClient(clientId)`,
		`function deleteClient(clientId, clientName)`,
		`const STALE_DEVICE_AGE_MS = 7 * 24 * 60 * 60 * 1000`,
		`function isClientStale(client)`,
		`function toggleStaleDevices(profileId)`,
		`not seen in 7+ days`,
		`Needs attention`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("accounts template missing hierarchy marker %q", marker)
		}
	}

	if strings.Contains(source, `id="tab-accounts"`) || strings.Contains(source, `id="tab-profiles">Profiles</button>`) {
		t.Fatal("admin Users page still exposes separate Accounts or Profiles tabs")
	}
}

func TestAdminUsesAutomationTerminologyAndAlignedFilters(t *testing.T) {
	toolsBytes, err := adminTemplates.ReadFile("admin_templates/tools.html")
	if err != nil {
		t.Fatalf("read tools template: %v", err)
	}
	baseBytes, err := adminTemplates.ReadFile("admin_templates/base.html")
	if err != nil {
		t.Fatalf("read base template: %v", err)
	}
	toolsSource := string(toolsBytes)
	baseSource := string(baseBytes)

	for _, marker := range []string{
		`<h1>Automations</h1>`,
		`Find an automation`,
		`Name, service, or automation type`,
		`Add Automation`,
		`.task-toolbar .form-input,`,
		`height: 2.5rem;`,
		`min-height: 2.5rem;`,
	} {
		if !strings.Contains(toolsSource, marker) {
			t.Fatalf("tools template missing automation UX marker %q", marker)
		}
	}
	if !strings.Contains(baseSource, "Automations") {
		t.Fatal("admin navigation does not use Automations terminology")
	}
}

func TestToolsPagePointsDeviceManagementToUsersHierarchy(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/tools.html")
	if err != nil {
		t.Fatalf("read tools template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`Device Management Has Moved`,
		`Devices now appear with their person and household`,
		`href="{{.BasePath}}/accounts"`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("tools template missing device relocation marker %q", marker)
		}
	}

	if strings.Contains(source, `id="clientManagementSection"`) || strings.Contains(source, `id="clientsList"`) {
		t.Fatal("Tools page still renders the old client management interface")
	}
}
