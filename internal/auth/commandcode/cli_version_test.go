package commandcode

import (
	"os"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	if compareSemver("1.88.2", "1.88.1") <= 0 {
		t.Fatal("1.88.2 should be greater than 1.88.1")
	}
	if compareSemver("1.88.1", "1.88.1") != 0 {
		t.Fatal("equal versions")
	}
	if compareSemver("0.18.10", "1.88.1") >= 0 {
		t.Fatal("0.18.10 should be less than 1.88.1")
	}
}

func TestNoteMinVersionRaisesFloor(t *testing.T) {
	ResetCLIVersionForTest()
	t.Cleanup(ResetCLIVersionForTest)

	cliVersionValue.Store("1.0.0")
	if !NoteMinVersion("1.2.3") {
		t.Fatal("expected bump")
	}
	if CurrentCLIVersion() != "1.2.3" {
		t.Fatalf("got %s", CurrentCLIVersion())
	}
	if NoteMinVersion("1.2.0") {
		t.Fatal("lower version must not bump")
	}
}

func TestParseUpgradeMinVersion(t *testing.T) {
	got := parseUpgradeMinVersion([]byte(`{"error":{"code":"upgrade_required","message":"out of date","minVersion":"0.18.10"}}`))
	if got != "0.18.10" {
		t.Fatalf("got %q", got)
	}
}

func TestHeaderCLIVersionEnvOverride(t *testing.T) {
	ResetCLIVersionForTest()
	t.Cleanup(func() {
		_ = os.Unsetenv("COMMANDCODE_CLI_VERSION")
		ResetCLIVersionForTest()
	})
	_ = os.Setenv("COMMANDCODE_CLI_VERSION", "9.9.9")
	if got := HeaderCLIVersion(map[string]string{"cli_version": "1.0.0"}); got != "9.9.9" {
		t.Fatalf("env should win, got %s", got)
	}
}

func TestHeaderCLIVersionAttrOverride(t *testing.T) {
	ResetCLIVersionForTest()
	t.Cleanup(ResetCLIVersionForTest)
	if got := HeaderCLIVersion(map[string]string{"cli_version": "2.3.4"}); got != "2.3.4" {
		t.Fatalf("got %s", got)
	}
}

func TestFetchLatestCLIVersionFromNPM(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	version, err := fetchLatestCLIVersion(t.Context())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !isValidSemver(version) {
		t.Fatalf("invalid version %q", version)
	}
	t.Logf("npm latest command-code = %s", version)
}
