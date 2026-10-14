package setupcore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestPageHasNoLeftoverPlaceholders(t *testing.T) {
	for name, list := range map[string][]step{"install": installSteps, "uninstall": uninstallSteps} {
		p := page(list)
		if strings.Contains(p, "/*STEPS*/") || strings.Contains(p, "/*LOGO*/") {
			t.Fatalf("%s page still has a placeholder (the logo appears in more than one view)", name)
		}
		if strings.Count(p, "data:image/png;base64,") < 2 {
			t.Fatalf("%s page: expected the logo in every view", name)
		}
		for _, s := range list {
			if !strings.Contains(p, s.en) || !strings.Contains(p, s.ru) {
				t.Fatalf("%s page misses step %q", name, s.en)
			}
		}
	}
}

func TestCopyStepWritesPayloadAndSkipsPlaceholder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Hy2Tun")
	in := &installer{dir: dir, payload: fstest.MapFS{
		"payload/.keep":         {Data: []byte("x")},
		"payload/Hy2Tun.exe":    {Data: []byte("app")},
		"payload/Uninstall.exe": {Data: []byte("uninst")},
		"payload/wintun.dll":    {Data: []byte("dll")},
	}}
	if err := installSteps[1].do(in); err != nil { // "Copying files…"
		t.Fatal(err)
	}
	for name, want := range map[string]string{"Hy2Tun.exe": "app", "Uninstall.exe": "uninst", "wintun.dll": "dll"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".keep")); err == nil {
		t.Fatal("the placeholder file must not be installed")
	}
}

func TestCopyStepFailsWithoutPayload(t *testing.T) {
	in := &installer{dir: filepath.Join(t.TempDir(), "x")}
	if err := installSteps[1].do(in); err == nil {
		t.Fatal("an installer without files must fail loudly")
	}
	in.payload = fstest.MapFS{"payload/.keep": {Data: []byte("x")}}
	if err := installSteps[1].do(in); err == nil {
		t.Fatal("a payload with only the placeholder must fail loudly")
	}
}

func TestRunsFromInstallDir(t *testing.T) {
	dir := `C:\Program Files\Hy2Tun`
	if !runsFromInstallDir(`C:\Program Files\Hy2Tun\Uninstall.exe`, dir) {
		t.Fatal("the installed uninstaller must relocate itself")
	}
	if !runsFromInstallDir(`c:\program files\hy2tun\whatever-name.exe`, dir) {
		t.Fatal("any file name inside the folder must relocate (case-insensitive)")
	}
	if runsFromInstallDir(`C:\Users\u\AppData\Local\Temp\Hy2Tun-Uninstall-1.exe`, dir) {
		t.Fatal("the temporary copy must not relocate again")
	}
}

func TestUninstallSettingsStepIsOptional(t *testing.T) {
	last := uninstallSteps[len(uninstallSteps)-1]
	if last.when == nil {
		t.Fatal("deleting settings must be conditional")
	}
	if last.when(&installer{}) {
		t.Fatal("settings must be kept unless the user asked to delete them")
	}
	if !last.when(&installer{deleteData: true}) {
		t.Fatal("settings must be deleted when the user asked for it")
	}
}

func TestRelocatedCopyDoesNotInheritInstallDir(t *testing.T) {
	c := relocationCommand(`C:\Temp\Hy2Tun-Uninstall-1.exe`)
	if c.Dir == "" || c.Dir != os.TempDir() {
		t.Fatalf("the temporary uninstaller must start in %q, not inherit the cwd (got %q)", os.TempDir(), c.Dir)
	}
}

func TestIsEmptyDir(t *testing.T) {
	d := t.TempDir()
	if !isEmptyDir(d) {
		t.Fatal("a fresh directory is empty")
	}
	os.WriteFile(filepath.Join(d, "x"), []byte("1"), 0o644)
	if isEmptyDir(d) {
		t.Fatal("a directory with a file is not empty")
	}
	if isEmptyDir(filepath.Join(d, "missing")) {
		t.Fatal("a missing directory is not an empty directory")
	}
}

// Reproduces the field failure: a folder that is the current directory of a
// running process cannot be removed, and a child inherits that directory.
func TestFolderInUseAsCwdCannotBeRemoved(t *testing.T) {
	holder := t.TempDir()
	child := exec.Command("ping", "-n", "4", "127.0.0.1")
	child.Dir = holder // what the buggy relocation did implicitly
	if err := child.Start(); err != nil {
		t.Skip("ping not available")
	}
	defer child.Process.Kill()
	time.Sleep(300 * time.Millisecond)
	if err := os.RemoveAll(holder); err == nil {
		if _, serr := os.Stat(holder); serr != nil {
			t.Skip("this Windows version allowed deleting a cwd")
		}
	}
	// the fix: the same kind of child, started through relocationCommand's
	// neutral directory, does not pin the folder
	other := t.TempDir()
	c2 := relocationCommand("ping")
	c2.Args = []string{"ping", "-n", "4", "127.0.0.1"}
	if err := c2.Start(); err != nil {
		t.Skip("ping not available")
	}
	defer c2.Process.Kill()
	time.Sleep(300 * time.Millisecond)
	if err := os.RemoveAll(other); err != nil {
		t.Fatalf("a child started in the neutral directory must not pin %s: %v", other, err)
	}
}
