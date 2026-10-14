package setupcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
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
