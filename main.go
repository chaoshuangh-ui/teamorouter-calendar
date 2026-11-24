package main

import (
	"log"
	"sync"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/windows/sdk/winapi"
	"github.com/golang-gui/goui/theme/modern"
	"github.com/golang-gui/goui/ui"
)

func main() {
	model := &appModel{selected: -1}
	s := loadSettings()
	model.locked = s.Locked
	model.winX, model.winY = s.X, s.Y
	if s.Zoom > 0 {
		model.zoom = float32(s.Zoom)
	} else {
		model.zoom = 1
	}
	if d, err := loadData(); err == nil {
		model.data = d
	}
	model.panel = newPanelState()
	refreshStop := make(chan struct{})

	err := ui.Run("com.teamorouter.calendar", func(app ui.App) ui.RootView {
		model.app = app

		// Attach the window to the desktop layer, without stealing focus.
		// A desktop child is covered by every normal application window and is
		// revealed by "show desktop", instead of floating above everything.
		// Only one attach request may be queued, but a failed attempt is retried
		// on the next paint instead of being consumed forever by sync.Once.
		var (
			attachMu     sync.Mutex
			attachDone   bool
			attachQueued bool
		)
		attachToDesktop := func() {
			attachMu.Lock()
			if attachDone || attachQueued {
				attachMu.Unlock()
				return
			}
			attachQueued = true
			attachMu.Unlock()
			app.Post(func() {
				defer func() {
					attachMu.Lock()
					attachQueued = false
					attachMu.Unlock()
				}()
				win := app.FindWindow("main")
				if win == nil {
					return // not ready yet; the next paint will retry
				}
				hwnd := winapi.HWND(win.PlatformWindow().NativeHandle())
				parent := findDesktopWindow()
				if parent == 0 {
					return // Explorer may not have created Progman/WorkerW yet
				}
				model.hwnd = hwnd
				if style, err := winapi.GetWindowLong(hwnd, winapi.GWL_EXSTYLE); err == nil {
					_, _ = winapi.SetWindowLong(hwnd, winapi.GWL_EXSTYLE,
						style|winapi.LONG(winapi.WS_EX_NOACTIVATE))
				}
				if parentOf(hwnd) != parent {
					// A top-level WS_POPUP window is not really reparented by
					// SetParent; it must carry WS_CHILD first. Clearing the
					// high WS_POPUP bit avoids an int32 constant overflow.
					if style, err := winapi.GetWindowLong(hwnd, winapi.GWL_STYLE); err == nil {
						_, _ = winapi.SetWindowLong(hwnd, winapi.GWL_STYLE,
							(style&winapi.LONG(0x7FFFFFFF))|winapi.LONG(winapi.WS_CHILD))
					}
					_, _ = winapi.SetParent(hwnd, parent)
				}
				// Park it in the top-right corner of the primary monitor.
				x, y := 24, 24
				if dpi, err := winapi.GetDpiForWindow(hwnd); err == nil && dpi > 0 {
					if sw, err := winapi.GetSystemMetricsForDpi(0, dpi); err == nil {
						x = sw - int(winW*shrink*model.zoom*float32(dpi)/96) - 24
					}
				}
				if model.winX >= 0 && model.winY >= 0 {
					x, y = model.winX, model.winY
				}
				// Saved positions are screen coordinates, but SetWindowPos
				// on a child window expects parent client coordinates.
				px, py := screenToParent(hwnd, x, y)
				// Size the native window to the saved zoom so the rounded frame
				// and the content scale together from the very first paint.
				// For a child window hWndInsertAfter is ignored.
				zw, zh := int(winW*model.zoom+0.5), int(winH*model.zoom+0.5)
				_ = winapi.SetWindowPos(hwnd, winapi.HWND(0), px, py, zw, zh,
					winapi.SWP_NOACTIVATE)
				attachMu.Lock()
				attachDone = true
				attachMu.Unlock()
			})
		}

		// Reattach if Explorer restarts and replaces the desktop host.
		reattachToDesktop := func() {
			app.Post(func() {
				parent := findDesktopWindow()
				if parent == 0 {
					return
				}
				if model.hwnd == 0 || parentOf(model.hwnd) != parent {
					attachMu.Lock()
					attachDone = false
					attachMu.Unlock()
					attachToDesktop()
				}
			})
		}

		// Background refresh: data + live balance every minute. It also retries
		// the desktop attach in case the first paint ran before Explorer's
		// desktop host existed.
		go func() {
			attachToDesktop()
			retry := time.NewTimer(3 * time.Second)
			defer retry.Stop()
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-refreshStop:
					return
				case <-retry.C:
					attachToDesktop()
				case <-ticker.C:
					reattachToDesktop()
					d, err := loadData()
					if err != nil {
						continue
					}
					app.Post(func() {
						newest := ""
						if model.data != nil && len(model.data.Months) > 0 {
							newest = model.data.Months[0].Key
						}
						model.data = d
						// The newest page must stay the system's current month.
						// Only drop an open day popup when the month window really
						// shifted; a routine refresh must not close what is read.
						windowChanged := newest != "" && len(d.Months) > 0 && d.Months[0].Key != newest
						if windowChanged {
							model.monthIdx = 0
							model.selected = -1
						}
						if model.monthIdx >= len(d.Months) {
							model.monthIdx = 0
							model.selected = -1
						}
						if model.widget != nil {
							model.widget.RequestPaint()
						}
					})
				}
			}
		}()

		// Work-time + weather tracker. It ticks every second and asks the GUI
		// thread to repaint the panel while it is visible.
		model.tracker = &workTracker{
			panel:      model.panel,
			weatherReq: make(chan struct{}, 1),
			notify: func() {
				app.Post(func() {
					if model.page == 1 && model.widget != nil {
						model.widget.RequestPaint()
					}
				})
			},
		}
		model.trackerStop = make(chan struct{})
		model.trackerDone = make(chan struct{})
		go func() {
			defer close(model.trackerDone)
			model.tracker.run(model.trackerStop)
		}()

		// The memo input window is a normal, activatable window so the OS
		// delivers IME keystrokes for Chinese text. Hide it on first paint.
		var memoHidden sync.Once
		hideMemo := func(gui.Painter, geometry.Rectangle) {
			memoHidden.Do(func() {
				app.Post(func() {
					if win := app.FindWindow("memo-input"); win != nil {
						win.RequestState(gui.WindowStateHidden)
					}
				})
			})
		}

		return ui.Root().
			StyleSheet(modern.FromSettings(app.Settings())).
			Windows(
				ui.Window("main").
					Title("TeamoRouter Calendar").
					Chrome(ui.WindowChromeNone).
					Transparent(true).
					SkipTaskbar(true).
					Size(320, 416).
					OnAfterPaint(func(gui.Painter, geometry.Rectangle) {
						attachToDesktop()
					}).
					Content(newCalendarView(model)),
				ui.Window("memo-input").
					Title("添加任务").
					Chrome(ui.WindowChromeNative).
					SkipTaskbar(true).
					Size(320, 150).
					Center(true).
					OnAfterPaint(hideMemo).
					Content(newMemoInputView(model)),
			)
	})
	if err != nil {
		log.Fatal(err)
	}
	// Stop the background loops and let the tracker flush a final snapshot
	// before the process exits.
	close(refreshStop)
	if model.trackerStop != nil {
		close(model.trackerStop)
	}
	if model.trackerDone != nil {
		select {
		case <-model.trackerDone:
		case <-time.After(2 * time.Second):
		}
	}
	// The tracker may be blocked in a weather request when the 2s grace period
	// expires. save() is mutex-protected, so flushing here is always safe.
	model.panel.save()
}
