//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTunGUIDIsStable(t *testing.T) {
	a, b := tunGUID("Hy2Tun"), tunGUID("Hy2Tun")
	if a.String() != b.String() {
		t.Fatalf("GUID changed between calls: %s != %s", a.String(), b.String())
	}
	if a.String() == "" || a.String() == "{00000000-0000-0000-0000-000000000000}" {
		t.Fatalf("invalid GUID: %q", a.String())
	}
	if a.Data3>>12 != 3 {
		t.Fatalf("version = %x, want 3", a.Data3>>12)
	}
	if a.Data4[0]&0xc0 != 0x80 {
		t.Fatalf("variant = %x, want RFC 4122", a.Data4[0])
	}
	if tunGUID("Hy2Tun").String() != tunGUID("hy2tun").String() {
		t.Fatal("adapter names are case-insensitive, GUIDs must be too")
	}
	if tunGUID("Hy2Tun").String() == tunGUID("Hy2Tun2").String() {
		t.Fatal("different adapter names got the same GUID")
	}
}

func TestTunCleanupScript(t *testing.T) {
	guid := tunGUID("Hy2Tun")
	s := tunCleanupScript("Hy2Tun", guid)
	for _, want := range []string{
		"Remove-NetAdapter -InputObject $_",
		"$pat='^Hy2Tun( \\d+)?$'",
		"$keep='" + guid.String() + "'",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("cleanup script lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "$_.PSChildName -ne $keep") {
		t.Fatal("the script must keep the entry of our own GUID")
	}

	quoted := tunCleanupScript("it's", guid)
	if !strings.Contains(quoted, "$pat='^it''s( \\d+)?$'") {
		t.Fatalf("single quote was not escaped for PowerShell:\n%s", quoted)
	}
}

// TestTunCleanupScriptParses feeds the script to the PowerShell parser, so a
// quoting mistake is caught without touching a real adapter.
func TestTunCleanupScriptParses(t *testing.T) {
	for _, name := range []string{"Hy2Tun", "it's", `a"b`} {
		file := filepath.Join(t.TempDir(), "cleanup.ps1")
		if err := os.WriteFile(file, []byte(tunCleanupScript(name, tunGUID(name))), 0o600); err != nil {
			t.Fatal(err)
		}
		check := "$e=$null; [System.Management.Automation.Language.Parser]::ParseFile('" +
			strings.ReplaceAll(file, "'", "''") +
			"', [ref]$null, [ref]$e) > $null; if ($e.Count) { $e | ForEach-Object { $_.Message }; exit 1 }"
		if out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", check).CombinedOutput(); err != nil {
			t.Fatalf("name %q: %v: %s", name, err, out)
		}
	}
}
