package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the real scripts in a disposable directory with systemctl stubbed out.
// This catches bash -e, marker and rollback bugs that a fake SSH server cannot.
func runAdminBash(t *testing.T, script string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		bash = filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, err := os.Stat(bash); err != nil {
			t.Skip("bash not installed")
		}
	}
	cmd := exec.Command(bash, "-se")
	cmd.Env = append(os.Environ(), "TMPDIR="+filepath.ToSlash(t.TempDir()))
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const adminBashSandbox = `
sandbox=$(mktemp -d)
trap 'rm -rf "$sandbox"' EXIT
printf 'auth:\n  type: userpass\n  userpass:\n    Admin: original\n' > "$sandbox/config.yaml"
cp "$sandbox/config.yaml" "$sandbox/original"
chown() { return 0; }
getent() { return 1; }
hysteria() { return 127; }
sleep() { :; }
`

func adminSandboxScript(script string) string {
	script = strings.ReplaceAll(script, "/etc/hysteria/", "$sandbox/")
	return strings.ReplaceAll(script, "/run/hy2tun-admin.", "$sandbox/hy2tun-admin.")
}

func TestAdminBashStatusSurvivesInactiveAndMissingVersion(t *testing.T) {
	script := adminBashSandbox + `
systemctl() { echo inactive; return 3; }
` + adminSandboxScript(adminScriptStatus())
	out, err := runAdminBash(t, script)
	if err != nil || !strings.Contains(out, "HY2TUN_PAYLOAD\nservice=inactive\nconfig=config_ok\nversion=\nHY2TUN_CONFIG\n") {
		t.Fatalf("inactive status lost its payload: %v\n%s", err, out)
	}
}

func TestAdminBashWritesWithoutStatsAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		restart    string
		active     string
		wantCode   string
		wantConfig string
	}{
		{"success", "return 0", "return 0", "", "changed"},
		{"restart failure", `[ "$restarts" -gt 1 ]`, "return 0", "service_down", "original"},
		{"startup failure", "return 0", `[ "$restarts" -gt 1 ]`, "service_down", "original"},
		{"rollback failure", "return 1", "return 1", "rollback_failed", "original"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := "auth:\n  type: userpass\n  userpass:\n    Admin: changed\n"
			script := adminBashSandbox + `
restarts=0
systemctl() {
  if [ "$1" = restart ]; then
    restarts=$((restarts+1))
    ` + tc.restart + `
  else
    ` + tc.active + `
  fi
}
set +e
(
set -e
` + adminSandboxScript(adminScriptWrite(base64.StdEncoding.EncodeToString([]byte(config)))) + `
)
result=$?
set -e
grep -q 'Admin: ` + tc.wantConfig + `' "$sandbox/config.yaml"
echo "CHECKED:$result"
`
			out, err := runAdminBash(t, script)
			if err != nil {
				t.Fatalf("sandbox or config verification failed: %v\n%s", err, out)
			}
			if tc.wantCode == "" {
				if !strings.Contains(out, "HY2TUN_PAYLOAD\nok\nCHECKED:0") {
					t.Fatalf("write without trafficStats failed: %s", out)
				}
			} else if !strings.Contains(out, "HY2TUN_ERROR:"+tc.wantCode+"\nCHECKED:1") {
				t.Fatalf("rollback result missing: %s", out)
			}
		})
	}
}
