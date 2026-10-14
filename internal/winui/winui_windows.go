// Package winui holds the few Win32 helpers shared by the application window
// and the installer: following the system theme in the title bar and
// changing window styles of a window that a library created.
package winui

import (
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	dwmapi                = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttr  = dwmapi.NewProc("DwmSetWindowAttribute")
	user32                = windows.NewLazySystemDLL("user32.dll")
	procGetWindowLongPtrW = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW = user32.NewProc("SetWindowLongPtrW")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
)

// Theme describes the colours of the page that is shown in the window, so the
// native title bar can blend with it.
type Theme struct {
	Dark       bool
	Background uint32 // COLORREF 0x00BBGGRR
	Text       uint32
	Border     uint32
}

// DarkTheme / LightTheme mirror the CSS variables of the UI.
var (
	DarkTheme  = Theme{Dark: true, Background: 0x0015100e, Text: 0x00f5efec, Border: 0x00382c27}
	LightTheme = Theme{Dark: false, Background: 0x00f8f4f2, Text: 0x00211a17, Border: 0x00ece2dd}
)

// SystemTheme returns the theme matching the "apps" colour mode of Windows.
func SystemTheme() Theme {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return DarkTheme
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err == nil && v == 1 {
		return LightTheme
	}
	return DarkTheme
}

func setAttr(hwnd uintptr, attr uintptr, v uint32) {
	procDwmSetWindowAttr.Call(hwnd, attr, uintptr(unsafe.Pointer(&v)), 4)
}

// ApplyTheme paints the title bar and the border in the page colours
// (Windows 11 22000+); on older systems only the dark/light mode is applied.
func ApplyTheme(hwnd unsafe.Pointer, t Theme) {
	h := uintptr(hwnd)
	dark := uint32(0)
	if t.Dark {
		dark = 1
	}
	setAttr(h, 20, dark) // DWMWA_USE_IMMERSIVE_DARK_MODE
	setAttr(h, 19, dark) // the same switch on Windows 10 before 20H1
	setAttr(h, 35, t.Background)
	setAttr(h, 36, t.Text)
	setAttr(h, 34, t.Border)
}

const (
	gwlStyle      = ^uintptr(15) // -16
	wsMaximizeBox = 0x00010000
	wsThickFrame  = 0x00040000
	wsSysMenu     = 0x00080000
)

func changeStyle(hwnd unsafe.Pointer, remove, add uintptr) {
	h := uintptr(hwnd)
	st, _, _ := procGetWindowLongPtrW.Call(h, gwlStyle)
	st = (st &^ remove) | add
	procSetWindowLongPtrW.Call(h, gwlStyle, st)
	// SWP_NOSIZE|SWP_NOMOVE|SWP_NOZORDER|SWP_NOACTIVATE|SWP_FRAMECHANGED
	procSetWindowPos.Call(h, 0, 0, 0, 0, 0, 0x0001|0x0002|0x0004|0x0010|0x0020)
}

// MakeFixedSize removes the sizing border and the maximize button.
func MakeFixedSize(hwnd unsafe.Pointer) { changeStyle(hwnd, wsThickFrame|wsMaximizeBox, 0) }

var (
	procGetSystemMenu = user32.NewProc("GetSystemMenu")
	procEnableMenu    = user32.NewProc("EnableMenuItem")
)

// SetCloseEnabled greys out (or re-enables) the close button. The system menu
// stays, so the window keeps its title-bar icon and the minimize button.
func SetCloseEnabled(hwnd unsafe.Pointer, on bool) {
	const scClose, mfByCommand, mfEnabled, mfGrayed = 0xF060, 0x0, 0x0, 0x1
	menu, _, _ := procGetSystemMenu.Call(uintptr(hwnd), 0)
	if menu == 0 {
		return
	}
	flag := uintptr(mfByCommand | mfGrayed)
	if on {
		flag = mfByCommand | mfEnabled
	}
	procEnableMenu.Call(menu, scClose, flag)
}
