package main

import (
	"syscall"
	"time"
	"unsafe"

	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/windows/sdk/winapi"
	"github.com/golang-gui/goui/ui"
)

// newMemoInputView builds the small, activatable window used to type a task.
// Chinese IME works here because this window is a normal top-level window.
func newMemoInputView(m *appModel) ui.View {
	// Keep the state source outside the build callback: a View can be rebuilt,
	// which would otherwise drop in-progress text and strand the callbacks.
	text := ui.MakeState("")
	return ui.Build(func() ui.View {
		commit := func() {
			m.panel.addTodo(text.Get(), time.Now().Format("2006-01-02"))
			text.Set("")
			m.hideMemoInput()
		}

		field := ui.TextInput().
			ID("memo-text").
			BindText(&text).
			Placeholder("输入任务，回车添加（支持中文）").
			OnSubmit(commit)

		confirm := ui.Button("添加").
			ID("memo-add").
			OnClick(commit)

		cancel := ui.Button("取消").
			ID("memo-cancel").
			OnClick(func() {
				text.Set("")
				m.hideMemoInput()
			})

		return ui.VBox(
			ui.Label("新任务").Style("memo-title"),
			field,
			ui.HBox(cancel, confirm).Spacing(10).MainAlign(layout.MainEnd),
		).Spacing(12).CrossAlign(layout.CrossStretch).Padding(16)
	})
}

// forceForeground makes the memo input window the OS foreground window so the
// IME delivers keystrokes to it. The desktop calendar is WS_EX_NOACTIVATE, so
// clicking it does not change the foreground process; plain SetForegroundWindow
// can be rejected by the Windows foreground lock. Attaching to the foreground
// thread first is the standard reliable workaround.
func forceForeground(hwnd winapi.HWND) {
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procGetFG := user32.NewProc("GetForegroundWindow")
	procGetWTP := user32.NewProc("GetWindowThreadProcessId")
	procAttach := user32.NewProc("AttachThreadInput")
	procBring := user32.NewProc("BringWindowToTop")
	procSetFG := user32.NewProc("SetForegroundWindow")
	procGetCT := kernel32.NewProc("GetCurrentThreadId")

	fg, _, _ := procGetFG.Call()
	var pid uint32
	fgTid, _, _ := procGetWTP.Call(fg, uintptr(unsafe.Pointer(&pid)))
	myTid, _, _ := procGetCT.Call()
	targetTid, _, _ := procGetWTP.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))

	if fgTid != 0 && myTid != fgTid {
		procAttach.Call(myTid, fgTid, 1)
		defer procAttach.Call(myTid, fgTid, 0)
	}
	if targetTid != 0 && myTid != targetTid {
		procAttach.Call(myTid, targetTid, 1)
		defer procAttach.Call(myTid, targetTid, 0)
	}
	procBring.Call(uintptr(hwnd))
	procSetFG.Call(uintptr(hwnd))
}

func (m *appModel) hideMemoInput() {
	m.app.Post(func() {
		if win := m.app.FindWindow("memo-input"); win != nil {
			win.RequestState(gui.WindowStateHidden)
		}
		// The input bar belongs to the main window: clear its "typing" state
		// on both commit and cancel, not only when switching tabs.
		if w := m.widget; w != nil {
			w.todoInputActive = false
			w.todoInputText = ""
			w.RequestPaint()
		}
	})
}
