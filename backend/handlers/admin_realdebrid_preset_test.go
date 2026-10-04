package handlers

import (
	"os"
	"os/exec"
	"testing"
)

func TestRealDebridRankingPresetQuickAdd(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to exercise the settings template JavaScript")
	}
	filtering := SettingsSchema["filtering"].(map[string]interface{})
	fields := filtering["fields"].(map[string]interface{})
	preset := fields["nonPreferredTerms"].(map[string]interface{})["quickAddTerm"].(string)
	cmd := exec.Command(node, "--test", "testdata/admin-realdebrid-preset.test.cjs")
	cmd.Env = append(os.Environ(), "RD_PRESET_TERM="+preset)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("settings preset tests failed: %v\n%s", err, output)
	}
}
