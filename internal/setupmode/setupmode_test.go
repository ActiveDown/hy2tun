package setupmode

import "testing"

func TestIsUninstall(t *testing.T) {
	cases := []struct {
		args []string
		exe  string
		want bool
	}{
		{nil, `C:\Users\u\Downloads\Hy2Tun-0.5.0-setup-windows-amd64.exe`, false},
		{nil, `C:\Users\u\Downloads\Hy2Tun-0.5.0-setup.exe`, false},
		{[]string{"/uninstall"}, `C:\Downloads\setup.exe`, true},
		{[]string{"/UNINSTALL"}, `C:\Downloads\setup.exe`, true},
		// the case from the field: Uninstall.exe started by double click, no arguments
		{nil, `C:\Program Files\Hy2Tun\Uninstall.exe`, true},
		{nil, `C:\Users\u\AppData\Local\Temp\Hy2Tun-Uninstall-1234.exe`, true},
		{nil, `C:\Program Files\Hy2Tun\uninstall.EXE`, true},
	}
	for _, c := range cases {
		if got := IsUninstall(c.args, c.exe); got != c.want {
			t.Errorf("IsUninstall(%v, %q) = %v, want %v", c.args, c.exe, got, c.want)
		}
	}
}

func TestSamePath(t *testing.T) {
	if !SamePath(`C:\Program Files\Hy2Tun\Uninstall.exe`, `c:\program files\hy2tun\UNINSTALL.EXE`) {
		t.Fatal("paths differ only by case")
	}
	if SamePath(`C:\a\x.exe`, `C:\b\x.exe`) {
		t.Fatal("different folders")
	}
}
