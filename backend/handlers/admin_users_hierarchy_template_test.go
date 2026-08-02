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

func TestAdminSettingsUsesHierarchyScopeTree(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Fatalf("read settings template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`id="settingsScopeTree"`,
		`class="settings-scope-tree"`,
		`function renderSettingsScopeTree()`,
		`function renderSettingsPersonScope(profile)`,
		`function selectSettingsScope(kind, profileId, clientId = '')`,
		`function toggleSettingsServer(event)`,
		`function toggleSettingsPerson(event, profileId)`,
		`let settingsScopeServerExpanded = false;`,
		`class="settings-scope-server-line"`,
		`const STALE_SETTINGS_DEVICE_AGE_MS = 7 * 24 * 60 * 60 * 1000`,
		`function isSettingsDeviceStale(client)`,
		`not seen in 7+ days`,
		`loadSettingsScopeClients()`,
		`id="settingsSearch" class="form-input" placeholder="Search settings..." autocomplete="off"`,
		`readonly aria-autocomplete="none"`,
		`function handleSettingsSearchInput(input)`,
		`data-bwignore="true" data-protonpass-ignore="true" data-form-type="other"`,
		`#clientSelector { display: none !important; }`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("settings template missing hierarchy scope marker %q", marker)
		}
	}

	if strings.Contains(source, `<select id="userSelector" class="form-select"`) ||
		strings.Contains(source, `<select id="clientSelector" class="form-select"`) {
		t.Fatal("settings page still exposes dropdown scope selectors")
	}
	if strings.Contains(source, `name="mediastorm-settings-filter"`) {
		t.Fatal("settings search retains a stable field name that autofill can target")
	}
	if strings.Contains(source, `const selectingCurrentPerson =`) {
		t.Fatal("selecting a person still toggles that person's device branch")
	}
	if strings.Contains(source, `households.forEach(account => expandedSettingsHouseholds.add(account.id))`) {
		t.Fatal("settings households are still expanded on initial render")
	}
	if strings.Contains(source, `.settings-scope-tree::-webkit-scrollbar`) ||
		strings.Contains(source, `max-height: calc(100vh - 92px)`) ||
		strings.Contains(source, `.settings-scope-tree { max-height:`) {
		t.Fatal("settings scope hierarchy still creates an internal scroll container")
	}
}

func TestAdminSettingsSurfacesAndReviewsScopedCustomizations(t *testing.T) {
	templateBytes, err := adminTemplates.ReadFile("admin_templates/settings.html")
	if err != nil {
		t.Fatalf("read settings template: %v", err)
	}
	source := string(templateBytes)

	for _, marker := range []string{
		`class="settings-scope-custom"`,
		`function profileCustomizationCount(profileId)`,
		`function settingsScopeCustomizationBadge(count, label = 'custom')`,
		`Review Customizations`,
		`Review profile customizations`,
		`Review device customizations`,
		`Use Parent Defaults`,
		`function updateProfileSaveImpact(changedGroupKeys)`,
		`function updateClientSaveImpact()`,
		`class="settings-impact-banner"`,
		`Those custom values remain unchanged and continue to take precedence over server defaults.`,
		`Those device values remain unchanged and continue to take precedence over profile defaults.`,
		`if (selectedClientId && clientSettings !== null)`,
		`'/api/clients/' + encodeURIComponent(selectedClientId) + '/settings'`,
		`showToast('Device settings saved successfully')`,
	} {
		if !strings.Contains(source, marker) {
			t.Fatalf("settings template missing scoped-customization marker %q", marker)
		}
	}

	if strings.Contains(source, `Copy to Profiles`) || strings.Contains(source, `Copy to Devices`) {
		t.Fatal("settings page still describes resetting child overrides as copying settings")
	}
	if strings.Contains(source, `fetch(basePath + '/api/settings/propagate'`) {
		t.Fatal("settings page still invokes the legacy bulk propagation endpoint")
	}
}
