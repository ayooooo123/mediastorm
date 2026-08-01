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
		`function renderPersonRow(profile)`,
		`function renderDeviceRow(client, orphaned)`,
		`function reassignClient(clientId, newProfileId`,
		`function pingClient(clientId)`,
		`function deleteClient(clientId, clientName)`,
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
