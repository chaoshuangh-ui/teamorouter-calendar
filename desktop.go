package main

import (
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/golang-gui/goui/platform/windows/sdk/winapi"
)

// The GOUI winapi wrapper does not expose a few of the user32 calls needed to
// attach a window to the desktop layer, so declare the small subset directly.
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procFindWindowW         = user32.NewProc("FindWindowW")
	procFindWindowExW       = user32.NewProc("FindWindowExW")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
	procGetParent           = user32.NewProc("GetParent")
	procGetLastInputInfo    = user32.NewProc("GetLastInputInfo")
	procGetTickCount        = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// idleDuration reports how long any keyboard or mouse input has been absent.
// GetLastInputInfo covers both devices, so typing keeps the clock running even
// when the mouse stays still.
func idleDuration() (time.Duration, bool) {
	lii := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if r == 0 {
		return 0, false
	}
	now, _, _ := procGetTickCount.Call()
	// Unsigned subtraction handles the ~49 day tick counter wraparound.
	return time.Duration(uint32(now)-lii.dwTime) * time.Millisecond, true
}

// parentOf returns the desktop parent of a child window, or 0 on failure.
func parentOf(h winapi.HWND) winapi.HWND {
	r, _, _ := procGetParent.Call(uintptr(h))
	return winapi.HWND(r)
}

// screenToParent converts a screen position into the coordinate space used by
// SetWindowPos for a child window. It matters on multi-monitor setups where
// the desktop parent does not start at the virtual-desktop origin.
func screenToParent(h winapi.HWND, x, y int) (int, int) {
	parent := parentOf(h)
	if parent == 0 {
		return x, y
	}
	p := winapi.POINT{X: int32(x), Y: int32(y)}
	if winapi.ScreenToClient(parent, &p) != 0 {
		return int(p.X), int(p.Y)
	}
	return x, y
}

// parentToScreen is the inverse of screenToParent, used before persisting a
// dragged position so the value stays meaningful across display changes.
func parentToScreen(h winapi.HWND, x, y int) (int, int) {
	parent := parentOf(h)
	if parent == 0 {
		return x, y
	}
	p := winapi.POINT{X: int32(x), Y: int32(y)}
	if winapi.ClientToScreen(parent, &p) != 0 {
		return int(p.X), int(p.Y)
	}
	return x, y
}

func findWindow(className, windowName string) winapi.HWND {
	var cls, name *uint16
	if className != "" {
		cls, _ = syscall.UTF16PtrFromString(className)
	}
	if windowName != "" {
		name, _ = syscall.UTF16PtrFromString(windowName)
	}
	r, _, _ := procFindWindowW.Call(
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(name)),
	)
	return winapi.HWND(r)
}

func findWindowEx(parent, after winapi.HWND, className, windowName string) winapi.HWND {
	var cls, name *uint16
	if className != "" {
		cls, _ = syscall.UTF16PtrFromString(className)
	}
	if windowName != "" {
		name, _ = syscall.UTF16PtrFromString(windowName)
	}
	r, _, _ := procFindWindowExW.Call(
		uintptr(parent),
		uintptr(after),
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(name)),
	)
	return winapi.HWND(r)
}

const (
	msgCreateWorkerW = 0x052C
	smtoNormal       = 0x0000
)

var (
	// syscall.NewCallback consumes a limited, non-GC'd slot, so create the
	// EnumWindows callback once and reuse it for every desktop lookup.
	enumDesktopMu   sync.Mutex
	enumDesktopHost winapi.HWND
	enumDesktopProc = syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if findWindowEx(winapi.HWND(hwnd), 0, "SHELLDLL_DefView", "") != 0 {
			enumDesktopHost = winapi.HWND(hwnd)
		}
		return 1
	})
	createWorkerWOnce sync.Once
)

// findDesktopWindow returns a window that lives on the desktop layer. Making
// our window a child of it keeps the calendar on the desktop: normal
// application windows always paint above it, and "show desktop" reveals it.
func findDesktopWindow() winapi.HWND {
	progman := findWindow("Progman", "Program Manager")
	if progman == 0 {
		return 0
	}

	// Ask Explorer once to spawn the WorkerW used for desktop gadgets. Repeated
	// calls can create extra empty WorkerW windows on some Explorer versions.
	createWorkerWOnce.Do(func() {
		var result uintptr
		_, _, _ = procSendMessageTimeoutW.Call(
			uintptr(progman),
			msgCreateWorkerW,
			0,
			0,
			smtoNormal,
			1000,
			uintptr(unsafe.Pointer(&result)),
		)
	})

	// Attach to the window that hosts SHELLDLL_DefView (the desktop icons).
	// New children are added at the top of its Z order, so the calendar sits
	// above the icons but still below every normal application window. Using
	// the WorkerW *after* it would put us under the icon list, which swallows
	// all mouse clicks.
	enumDesktopMu.Lock()
	enumDesktopHost = 0
	_, _, _ = procEnumWindows.Call(enumDesktopProc, 0)
	desktop := enumDesktopHost
	enumDesktopMu.Unlock()

	if desktop != 0 {
		return desktop
	}
	// Older Windows versions keep SHELLDLL_DefView directly under Progman.
	return progman
}
