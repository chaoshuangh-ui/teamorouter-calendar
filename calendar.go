package main

import (
	"image/color"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/layout"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/platform/typography"
	"github.com/golang-gui/goui/platform/windows/sdk/winapi"
	"github.com/golang-gui/goui/ui"
)

// ---- layout constants (DIP) ----
const (
	winW, winH   float32 = 400, 520
	cardX, cardY float32 = 0, 0
	cardW, cardH float32 = winW, winH
	contentX     float32 = 28
	gridY        float32 = 132
	cellW, cellH float32 = 44, 46
	gapX, gapY   float32 = 6, 6
	gridCols             = 7
	gridRows             = 6
	weekdayY     float32 = 106
	legendY      float32 = 452
	footerY      float32 = 482
	navPrevX     float32 = 330
	navNextX     float32 = 362
	navY         float32 = 56
	navR         float32 = 14
	lockX        float32 = 362
	lockY        float32 = 30
	lockR        float32 = 11

	// Zoom controls live between the tabs and the lock icon. Clicking the
	// percentage label resets to 100%.
	zoomOutX, zoomInX float32 = 278, 326
	zoomY, zoomR      float32 = 30, 9
	zoomLabelX        float32 = 290
	zoomLabelY        float32 = 24
	zoomLabelW        float32 = 24
	zoomLabelH        float32 = 12

	// Popup geometry. The taller popup fits the day summary plus a quota
	// section for all three services.
	popupX, popupY           float32 = 28, 64
	popupW, popupH           float32 = 344, 424
	popupCloseX, popupCloseY float32 = 348, 88
	popupCloseR              float32 = 13

	// shrink scales the whole 400x520 design into a smaller window. The
	// design coordinates stay unchanged so hit-testing and layout stay in
	// one space; only the painter transform maps them to fewer pixels.
	shrink float32 = 0.8
)

var weekdayNames = [7]string{"M", "T", "W", "T", "F", "S", "S"}

// ---- hit-test actions ----
const (
	hitNone      = 0
	hitPrev      = 1
	hitNext      = 2
	hitClose     = 3
	hitLock      = 4
	hitZoomOut   = 5
	hitZoomIn    = 6
	hitZoomReset = 7
	hitDayMin    = 100
)

// appModel is the shared, mutable state. GUI callbacks are the only writers.
type appModel struct {
	data     *AppData
	monthIdx int  // 0 = current month, larger = older
	selected int  // -1 = no popup, otherwise day index in current month
	locked   bool // when true, the window cannot be dragged
	winX     int  // saved window position (screen coordinates), -1 = default
	winY     int
	widget   *calendarWidget
	hwnd     winapi.HWND
	page     int     // 0 = calendar, 1 = daily panel
	zoom     float32 // uniform content zoom, 1 = 100%
	panel    *panelState
	tracker  *workTracker
	app      ui.App

	trackerStop chan struct{}
	trackerDone chan struct{}
}

func (m *appModel) saveSettings() {
	saveSettings(settings{Locked: m.locked, X: m.winX, Y: m.winY, Zoom: float64(m.zoom)})
}

func (m *appModel) curMonth() *MonthData {
	if m.data == nil || len(m.data.Months) == 0 {
		return nil
	}
	if m.monthIdx < 0 {
		m.monthIdx = 0
	}
	if m.monthIdx >= len(m.data.Months) {
		m.monthIdx = len(m.data.Months) - 1
	}
	return &m.data.Months[m.monthIdx]
}

// designPos converts a widget-local pointer position into the 400x520 design
// space. The widget itself is resized with the zoom level, so scaleK already
// contains the zoom factor and one inverse transform covers the whole canvas,
// including the rounded frame.
func (w *calendarWidget) designPos(p geometry.Point) geometry.Point {
	k := w.scaleK
	if k <= 0 {
		k = shrink
	}
	return geometry.Point{X: p.X / k, Y: p.Y / k}
}

// applyWindowSize resizes the native window to match the current zoom. The
// rounded card fills the whole client, so the frame and the content scale
// together and the corner arcs stay intact at every level.
func (m *appModel) applyWindowSize() {
	if m.hwnd == 0 {
		return
	}
	z := m.zoom
	if z <= 0 {
		z = 1
	}
	w := int(winW*z + 0.5)
	h := int(winH*z + 0.5)
	// For a desktop child hWndInsertAfter is ignored; keep the current z-order
	// and position and only change the size.
	_ = winapi.SetWindowPos(m.hwnd, winapi.HWND(0), 0, 0, w, h,
		winapi.SWP_NOMOVE|winapi.SWP_NOACTIVATE|winapi.SWP_NOZORDER)
}

// setZoom clamps and persists a new zoom level. One-decimal steps keep the
// displayed percentage stable across repeated clicks or wheel notches.
func (w *calendarWidget) setZoom(z float32) {
	const (
		minZoom = 0.7
		maxZoom = 1.5
	)
	z = float32(math.Round(float64(z)*10) / 10)
	if z < minZoom {
		z = minZoom
	}
	if z > maxZoom {
		z = maxZoom
	}
	if z == w.model.zoom {
		return
	}
	w.model.zoom = z
	w.model.saveSettings()
	w.model.applyWindowSize()
	w.RequestPaint()
}

func pointInRect(p geometry.Point, x, y, w, h float32) bool {
	return p.X >= x && p.X <= x+w && p.Y >= y && p.Y <= y+h
}

func pointInCircle(p geometry.Point, cx, cy, r float32) bool {
	dx, dy := p.X-cx, p.Y-cy
	return dx*dx+dy*dy <= r*r
}

func (m *appModel) hitTest(widgetPos geometry.Point, popupOpen bool) int {
	w := m.widget
	if w == nil {
		return hitNone
	}
	dp := w.designPos(widgetPos)

	if popupOpen {
		if pointInCircle(dp, popupCloseX, popupCloseY, popupCloseR) {
			return hitClose
		}
		if pointInRect(dp, popupX, popupY, popupW, popupH) {
			return hitNone
		}
		return hitClose
	}
	// Header controls are checked first so they win over day cells.
	if pointInRect(dp, tabCalX, tabY, tabW, tabH) {
		return hitTabCal
	}
	if pointInRect(dp, tabPanelX, tabY, tabW, tabH) {
		return hitTabPanel
	}
	if pointInCircle(dp, lockX, lockY, lockR) {
		return hitLock
	}
	if pointInCircle(dp, zoomOutX, zoomY, zoomR) {
		return hitZoomOut
	}
	if pointInCircle(dp, zoomInX, zoomY, zoomR) {
		return hitZoomIn
	}
	if pointInRect(dp, zoomLabelX, zoomLabelY, zoomLabelW, zoomLabelH) {
		return hitZoomReset
	}
	if pointInCircle(dp, navPrevX, navY, navR) {
		return hitPrev
	}
	if pointInCircle(dp, navNextX, navY, navR) {
		return hitNext
	}
	if m.page == 1 {
		if pointInRect(dp, todoInputX, todoInputY, todoInputW, todoInputH+4) {
			return hitTodoInput
		}
		return w.panelHitTest(dp)
	}
	mo := m.curMonth()
	if mo == nil {
		return hitNone
	}
	for idx := 0; idx < mo.NumDays; idx++ {
		col := (mo.FirstWeekday + idx) % gridCols
		row := (mo.FirstWeekday + idx) / gridCols
		if row >= gridRows {
			break
		}
		x := contentX + float32(col)*(cellW+gapX)
		y := gridY + float32(row)*(cellH+gapY)
		if pointInRect(dp, x, y, cellW, cellH) {
			return hitDayMin + idx
		}
	}
	return hitNone
}

// ---- declarative view ----
type calendarView struct {
	ui.ViewBase[calendarView]
	model *appModel
}

func newCalendarView(m *appModel) *calendarView {
	v := &calendarView{model: m}
	v.Self = v
	return v
}

func (v *calendarView) Mount(ui.BuildContext) gui.Widget {
	w := &calendarWidget{model: v.model, workTipIdx: -1, scaleK: shrink}

	w.click = gui.NewClickEventController()
	w.click.ConnectPressed(func(_ gui.EventContext, info gui.ClickInfo) {
		// No dragging while locked or while the popup is open.
		if w.model.locked || w.model.selected >= 0 {
			return
		}
		// Drag only from the header strip; never from a day cell or the popup.
		dp := w.designPos(info.Position)
		if dp.Y > 90 {
			return
		}
		// Never start a drag on an interactive control (tabs, lock, zoom,
		// month arrows). Otherwise a 1px jitter during the press turns the
		// click into a drag and the button action is discarded.
		if w.model.hitTest(info.Position, false) != hitNone {
			return
		}
		m := w.model
		if m.hwnd == 0 {
			return
		}
		var cursor winapi.POINT
		if winapi.GetCursorPos(&cursor) == 0 {
			return
		}
		var r winapi.RECT
		if winapi.GetWindowRect(m.hwnd, &r) != nil {
			return
		}
		w.dragging = true
		w.dragMoved = false
		w.dragCursor = cursor
		w.dragBaseX, w.dragBaseY = screenToParent(m.hwnd, int(r.Left), int(r.Top))
	})
	w.click.ConnectReleased(func(gui.EventContext, gui.ClickInfo) {
		if w.dragging && w.model.hwnd != 0 {
			var r winapi.RECT
			if winapi.GetWindowRect(w.model.hwnd, &r) == nil {
				w.model.winX, w.model.winY = int(r.Left), int(r.Top)
				w.model.saveSettings()
			}
		}
		w.dragging = false
	})
	w.click.ConnectClicked(func(_ gui.EventContext, info gui.ClickInfo) {
		m := w.model
		if w.dragMoved {
			w.dragMoved = false
			return
		}
		popupOpen := m.selected >= 0
		// Any click other than a chart point dismisses a pinned tooltip.
		w.workTipIdx = -1
		switch a := m.hitTest(info.Position, popupOpen); {
		case a == hitTabCal:
			m.page = 0
			m.selected = -1
			w.todoInputActive = false
			w.panelScroll = 0
		case a == hitTabPanel:
			m.page = 1
			m.selected = -1
		case a == hitTodoInput:
			w.beginTodoInput()
		case a >= hitWorkPoint && a < hitTodoToggle:
			// Click pins the tooltip so the value stays readable after the
			// mouse moves away; any other click dismisses it.
			w.workTipIdx = a - hitWorkPoint
		case a >= hitTodoToggle && a < hitTodoDel:
			today := time.Now().Format("2006-01-02")
			items := m.panel.todayTodos(today)
			i := a - hitTodoToggle
			if i < len(items) {
				m.panel.toggleTodo(items[i].ID)
			}
			w.todoInputActive = false
		case a >= hitTodoDel:
			today := time.Now().Format("2006-01-02")
			items := m.panel.todayTodos(today)
			i := a - hitTodoDel
			if i < len(items) {
				m.panel.removeTodo(items[i].ID)
			}
			w.todoInputActive = false
		case a == hitPrev:
			if m.data != nil && m.monthIdx < len(m.data.Months)-1 {
				m.monthIdx++
			}
			m.selected = -1
		case a == hitNext:
			if m.monthIdx > 0 {
				m.monthIdx--
			}
			m.selected = -1
		case a == hitClose:
			m.selected = -1
		case a == hitLock:
			m.locked = !m.locked
			m.saveSettings()
		case a == hitZoomOut:
			w.setZoom(m.zoom - 0.1)
		case a == hitZoomIn:
			w.setZoom(m.zoom + 0.1)
		case a == hitZoomReset:
			w.setZoom(1)
		case a >= hitDayMin:
			m.selected = a - hitDayMin
		}
		w.RequestPaint()
	})
	w.click.ConnectPressedChanged(func(pressed bool) {
		w.pressed = pressed
		w.RequestPaint()
	})
	w.AddEventController(w.click)

	w.motion = gui.NewMotionEventController()
	w.motion.ConnectMotion(func(_ gui.EventContext, info gui.MotionInfo) {
		if w.dragging && w.model.hwnd != 0 {
			var cursor winapi.POINT
			if winapi.GetCursorPos(&cursor) != 0 {
				dx := int(cursor.X - w.dragCursor.X)
				dy := int(cursor.Y - w.dragCursor.Y)
				if dx != 0 || dy != 0 {
					w.dragMoved = true
					_ = winapi.SetWindowPos(w.model.hwnd, winapi.HWND(0),
						w.dragBaseX+dx, w.dragBaseY+dy, 0, 0,
						winapi.SWP_NOSIZE|winapi.SWP_NOACTIVATE)
				}
			}
		}
		a := w.model.hitTest(info.Position, w.model.selected >= 0)
		if a != w.hover {
			w.hover = a
			w.RequestPaint()
		}
	})
	w.AddEventController(w.motion)
	w.AddEventController(newTodoWheelController(w))

	v.model.widget = w
	return w
}

func (v *calendarView) Update(ui.BuildContext, gui.Widget)  {}
func (v *calendarView) Unmount(ui.BuildContext, gui.Widget) {}

// ---- custom widget ----
type calendarWidget struct {
	gui.WidgetBase
	model      *appModel
	scaleK     float32 // design-to-widget scale, shared by painting and hit tests
	click      *gui.ClickEventController
	motion     *gui.MotionEventController
	hover      int
	pressed    bool
	dragging   bool
	dragMoved  bool
	dragCursor winapi.POINT
	dragBaseX  int // window origin in parent coordinates when drag started
	dragBaseY  int

	todoInputActive bool
	todoInputText   string
	keyboard        *gui.KeyEventController

	panelScroll float32                        // vertical offset of the panel content
	workPoints  [workPointCount]geometry.Point // chart points in design space
	workDays    [workPointCount]int64          // seconds behind each point
	workBuckets map[string][]int64             // per-day two-hour work buckets
	workTipIdx  int                            // pinned work tooltip, -1 = none
}

func (w *calendarWidget) Measure(c layout.Constraint) layout.Measurement {
	if !w.Visible() {
		return layout.Measurement{}
	}
	return layout.Measured(c.Clamp(geometry.Size{Width: winW * shrink, Height: winH * shrink}))
}

func (w *calendarWidget) Snapshot() gui.WidgetInfo {
	info := w.WidgetBase.Snapshot()
	info.Role = gui.RoleBox
	info.Text = "TeamoRouter usage calendar"
	return info
}

// ---- palette ----
type heatStop struct {
	t       float32
	r, g, b byte
}

var heatStops = []heatStop{
	{0.00, 35, 41, 56},
	{0.15, 28, 78, 70},
	{0.35, 104, 84, 36},
	{0.55, 148, 78, 40},
	{0.75, 183, 58, 72},
	{1.00, 214, 48, 71},
}

func heatColor(cost, mx float64) (graphics.Color, graphics.Color) {
	if cost <= 0 {
		return graphics.RGB(35, 41, 56), graphics.RGB(122, 132, 152)
	}
	t := float32(math.Sqrt(cost / mx))
	if t > 1 {
		t = 1
	}
	var r, g, b byte
	for i := 0; i < len(heatStops)-1; i++ {
		a, c := heatStops[i], heatStops[i+1]
		if t <= c.t {
			u := (t - a.t) / (c.t - a.t)
			r = a.r + byte(float32(c.r-a.r)*u)
			g = a.g + byte(float32(c.g-a.g)*u)
			b = a.b + byte(float32(c.b-a.b)*u)
			break
		}
	}
	if r == 0 && g == 0 && b == 0 {
		last := heatStops[len(heatStops)-1]
		r, g, b = last.r, last.g, last.b
	}
	fg := graphics.RGB(150, 158, 176)
	if t > 0.42 {
		fg = graphics.RGB(255, 255, 255)
	}
	return graphics.RGB(r, g, b), fg
}

// ---- text helper ----
func text(p gui.Painter, s string, x, y, w, h, size float32, weight typography.FontWeight, col color.Color, align typography.TextAlignment) {
	if gui.App == nil || gui.App.Typography() == nil || w <= 0 || h <= 0 {
		return
	}
	tl, err := gui.App.Typography().NewTextLayout(s, typography.TextFormat{
		Font:      typography.FontInfo{Family: "Segoe UI", Size: size, Weight: weight},
		TextAlign: align,
		TextColor: col,
	}, w, h)
	if err != nil {
		return
	}
	defer tl.Destroy()
	_, th := tl.MeasureSize()
	p.DrawTextLayout(geometry.Point{X: x, Y: y + (h-th)/2}, tl)
}

// ---- painting ----
func (w *calendarWidget) Paint(p gui.Painter) {
	m := w.model

	// GOUI sizes the HWND in DIP: Size(320,416) becomes a 400x520 physical
	// client at this machine's 1.25 scale. The base painter also applies that
	// 1.25 scale. Apply `shrink` (widget DIP width / design width = 320/400)
	// so the 400x520 design maps exactly onto the widget bounds; the base
	// scale then fills the whole physical client with no white border.
	if k := w.Rect().Width / winW; k > 0 {
		w.scaleK = k // keep painting and hit-testing in the same scale
	}
	p.Save()
	defer p.Restore()
	p.SetTransform(geometry.Scale(w.scaleK, w.scaleK))

	// The card fills the entire client. Nothing is painted outside its
	// rounded outline, so the window is cut into a rounded rectangle: the
	// four corners are arc-shaped and transparent, the edges stay opaque.
	card := geometry.Rect(cardX, cardY, cardW, cardH)
	p.FillRoundRect(card, 22, graphics.RGBA(19, 22, 30, 255))
	p.DrawRoundRect(card, 22, 1, graphics.RGBA(54, 60, 78, 255))

	if m.page == 1 {
		if m.tracker != nil {
			w.paintPanel(p)
		}
		return
	}

	if m.data == nil {
		text(p, "LOADING USAGE...", contentX, 230, 320, 40, 14,
			typography.FontWeightMedium, graphics.RGB(150, 158, 176), typography.TextAlignBegin)
		return
	}

	w.paintHeader(p)
	w.paintWeekdays(p)
	w.paintGrid(p)
	w.paintLegend(p)
	if m.selected >= 0 {
		w.paintPopup(p)
	}
}

func (w *calendarWidget) paintHeader(p gui.Painter) {
	m := w.model
	mo := m.curMonth()
	if mo == nil {
		return
	}
	p.FillEllipse(geometry.Point{X: contentX + 4, Y: 34}, 3.5, 3.5, graphics.RGB(139, 124, 255))
	text(p, "TEAMOROUTER", contentX+16, 24, 200, 20, 9,
		typography.FontWeightSemibold, graphics.RGB(126, 135, 155), typography.TextAlignBegin)

	// Page switcher must be visible on the calendar page too, otherwise there
	// is no way for the user to discover the panel page.
	w.paintTabs(p)

	text(p, mo.Label, contentX, 44, 280, 32, 21,
		typography.FontWeightSemibold, graphics.RGB(242, 244, 248), typography.TextAlignBegin)

	// Chevrons are drawn as strokes rather than glyphs so their bounding box
	// is exactly centred on the circle. The newest month is the current one
	// (monthIdx 0), so "next" is dimmed there: no future months are reachable.
	atNewest := m.monthIdx <= 0
	atOldest := m.data == nil || m.monthIdx >= len(m.data.Months)-1
	for _, b := range []struct {
		x, y, r float32
		action  int
		left    bool
	}{
		{navPrevX, navY, navR, hitPrev, true},
		{navNextX, navY, navR, hitNext, false},
	} {
		enabled := true
		if b.left {
			enabled = !atOldest
		} else {
			enabled = !atNewest
		}
		bg := graphics.RGBA(38, 43, 57, 255)
		if enabled && w.hover == b.action {
			bg = graphics.RGBA(56, 62, 82, 255)
		}
		p.FillEllipse(geometry.Point{X: b.x, Y: b.y}, b.r, b.r, bg)
		stroke := graphics.RGB(200, 206, 220)
		if !enabled {
			stroke = graphics.RGB(88, 96, 116)
		}
		if b.left {
			p.DrawLine(geometry.Point{X: b.x + 3, Y: b.y - 6}, geometry.Point{X: b.x - 3, Y: b.y}, 2, stroke)
			p.DrawLine(geometry.Point{X: b.x - 3, Y: b.y}, geometry.Point{X: b.x + 3, Y: b.y + 6}, 2, stroke)
		} else {
			p.DrawLine(geometry.Point{X: b.x - 3, Y: b.y - 6}, geometry.Point{X: b.x + 3, Y: b.y}, 2, stroke)
			p.DrawLine(geometry.Point{X: b.x + 3, Y: b.y}, geometry.Point{X: b.x - 3, Y: b.y + 6}, 2, stroke)
		}
	}

	// Shared lock and zoom controls use the same geometry on both pages.
	w.paintLock(p)
	w.paintZoomControls(p)

	p.DrawLine(geometry.Point{X: contentX, Y: 90}, geometry.Point{X: 372, Y: 90}, 1, graphics.RGBA(54, 60, 78, 255))
}

// paintZoomControls draws the uniform zoom buttons shared by both pages.
func (w *calendarWidget) paintZoomControls(p gui.Painter) {
	for _, b := range []struct {
		x     float32
		hit   int
		minus bool
	}{
		{zoomOutX, hitZoomOut, true},
		{zoomInX, hitZoomIn, false},
	} {
		bg := graphics.RGBA(38, 43, 57, 255)
		if w.hover == b.hit {
			bg = graphics.RGBA(56, 62, 82, 255)
		}
		p.FillEllipse(geometry.Point{X: b.x, Y: zoomY}, zoomR, zoomR, bg)
		stroke := graphics.RGB(200, 206, 220)
		p.DrawLine(geometry.Point{X: b.x - 4, Y: zoomY}, geometry.Point{X: b.x + 4, Y: zoomY}, 2, stroke)
		if !b.minus {
			p.DrawLine(geometry.Point{X: b.x, Y: zoomY - 4}, geometry.Point{X: b.x, Y: zoomY + 4}, 2, stroke)
		}
	}
	label := itoa(int(math.Round(float64(w.model.zoom)*100))) + "%"
	col := graphics.RGB(126, 135, 155)
	if w.hover == hitZoomReset {
		col = graphics.RGB(236, 240, 248)
	}
	text(p, label, zoomLabelX, zoomLabelY, zoomLabelW, zoomLabelH, 9,
		typography.FontWeightSemibold, col, typography.TextAlignCenter)
}

func (w *calendarWidget) paintWeekdays(p gui.Painter) {
	for i, name := range weekdayNames {
		x := contentX + float32(i)*(cellW+gapX)
		col := graphics.RGB(110, 120, 142)
		if i >= 5 {
			col = graphics.RGB(139, 124, 255)
		}
		text(p, name, x, weekdayY, cellW, 20, 10,
			typography.FontWeightSemibold, col, typography.TextAlignCenter)
	}
}

func (w *calendarWidget) paintGrid(p gui.Painter) {
	m := w.model
	mo := m.curMonth()
	if mo == nil {
		return
	}
	for i := 0; i < mo.FirstWeekday; i++ {
		x := contentX + float32(i)*(cellW+gapX)
		p.FillRoundRect(geometry.Rect(x, gridY, cellW, cellH), 10, graphics.RGBA(27, 31, 42, 255))
	}
	mx := m.data.MaxCost
	for idx := 0; idx < mo.NumDays; idx++ {
		col := (mo.FirstWeekday + idx) % gridCols
		row := (mo.FirstWeekday + idx) / gridCols
		if row >= gridRows {
			break
		}
		x := contentX + float32(col)*(cellW+gapX)
		y := gridY + float32(row)*(cellH+gapY)
		rect := geometry.Rect(x, y, cellW, cellH)
		dd := mo.Days[idx]
		bg, fg := heatColor(dd.Cost, mx)
		if w.hover == hitDayMin+idx {
			bg = graphics.RGBA(70, 76, 98, 255)
		}
		p.FillRoundRect(rect, 10, bg)
		if dd.IsToday {
			p.DrawRoundRect(rect.Inset(1.5), 9, 1.5, graphics.RGB(180, 170, 255))
		}
		text(p, itoa(dd.Day), x, y+2, cellW, cellH-4, 13,
			typography.FontWeightSemibold, fg, typography.TextAlignCenter)
	}
}

func (w *calendarWidget) paintLegend(p gui.Painter) {
	m := w.model
	text(p, "LESS", contentX, legendY, 34, 20, 8,
		typography.FontWeightSemibold, graphics.RGB(100, 110, 130), typography.TextAlignBegin)
	barX, barW := contentX+42, float32(150)
	for i := 0; i < 80; i++ {
		t := float64(i) / 79
		bg, _ := heatColor(t*m.data.MaxCost, m.data.MaxCost)
		p.FillRect(geometry.Rect(barX+float32(i)*(barW/80), legendY+6, barW/80+0.6, 8), bg)
	}
	text(p, "MORE", barX+barW+8, legendY, 40, 20, 8,
		typography.FontWeightSemibold, graphics.RGB(100, 110, 130), typography.TextAlignBegin)
	text(p, "BALANCE  "+m.data.Balance, 250, legendY, 122, 20, 9,
		typography.FontWeightSemibold, graphics.RGB(180, 190, 210), typography.TextAlignEnd)

	p.DrawLine(geometry.Point{X: contentX, Y: 472}, geometry.Point{X: 372, Y: 472}, 1, graphics.RGBA(48, 54, 70, 255))
	text(p, "CLICK A DAY FOR DETAILS", contentX, footerY, 250, 20, 8,
		typography.FontWeightRegular, graphics.RGB(88, 98, 118), typography.TextAlignBegin)
}

func (w *calendarWidget) paintPopup(p gui.Painter) {
	m := w.model
	mo := m.curMonth()
	if mo == nil || m.selected >= mo.NumDays {
		return
	}
	dd := mo.Days[m.selected]

	p.FillRoundRect(geometry.Rect(cardX+2, cardY+2, cardW-4, cardH-4), 20, graphics.RGBA(10, 12, 18, 170))

	px, py, pw, ph := popupX, popupY, popupW, popupH
	p.FillRoundRect(geometry.Rect(px+2, py+6, pw, ph), 18, graphics.RGBA(0, 0, 0, 140))
	p.FillRoundRect(geometry.Rect(px, py, pw, ph), 18, graphics.RGBA(30, 34, 46, 255))
	p.DrawRoundRect(geometry.Rect(px, py, pw, ph), 18, 1, graphics.RGBA(70, 76, 98, 255))

	text(p, dd.Key, px+20, py+10, 240, 26, 13,
		typography.FontWeightSemibold, graphics.RGB(242, 244, 248), typography.TextAlignBegin)

	closeBg := graphics.RGBA(44, 49, 64, 255)
	if w.hover == hitClose {
		closeBg = graphics.RGBA(72, 78, 100, 255)
	}
	p.FillEllipse(geometry.Point{X: popupCloseX, Y: popupCloseY}, popupCloseR, popupCloseR, closeBg)
	// Draw the cross from the circle centre instead of using a glyph: font
	// ascent/descent would otherwise shift the × visually off centre.
	closeCol := graphics.RGB(190, 196, 210)
	const d = 4
	sw := float32(1.8)
	p.DrawLine(geometry.Point{X: popupCloseX - d, Y: popupCloseY - d},
		geometry.Point{X: popupCloseX + d, Y: popupCloseY + d}, sw, closeCol)
	p.DrawLine(geometry.Point{X: popupCloseX + d, Y: popupCloseY - d},
		geometry.Point{X: popupCloseX - d, Y: popupCloseY + d}, sw, closeCol)

	p.DrawLine(geometry.Point{X: px + 20, Y: py + 46}, geometry.Point{X: px + pw - 20, Y: py + 46}, 1, graphics.RGBA(60, 66, 84, 255))

	section := func(label string, y float32) {
		text(p, label, px+20, y, pw-40, 14, 9,
			typography.FontWeightSemibold, graphics.RGB(139, 124, 255), typography.TextAlignBegin)
	}
	service := func(label string, y float32) {
		text(p, label, px+20, y, pw-40, 14, 9,
			typography.FontWeightSemibold, graphics.RGB(180, 190, 210), typography.TextAlignBegin)
	}
	kv := func(label, value string, y float32) {
		text(p, label, px+20, y, 150, 18, 8,
			typography.FontWeightSemibold, graphics.RGB(120, 130, 150), typography.TextAlignBegin)
		text(p, value, px+170, y, pw-190, 18, 13,
			typography.FontWeightSemibold, graphics.RGB(236, 240, 248), typography.TextAlignEnd)
	}

	// --- this day's TeamoRouter usage ---
	section("DAY USAGE", py+52)
	modelName := "-"
	if len(dd.Models) > 0 {
		modelName = dd.Models[0].Name
		if len(dd.Models) > 1 {
			modelName += "  +" + itoa(len(dd.Models)-1)
		}
	}
	kv("TOKEN", formatTokens(dd.Tokens), py+68)
	kv("MODEL", modelName, py+92)
	kv("COST", "$"+fmtFloat(dd.Cost), py+116)
	kv("BALANCE", m.data.Balance, py+140)

	p.DrawLine(geometry.Point{X: px + 20, Y: py + 168}, geometry.Point{X: px + pw - 20, Y: py + 168}, 1, graphics.RGBA(60, 66, 84, 255))

	// --- current quota across the services ---
	section("QUOTA / BALANCE", py+176)

	codex := m.data.Codex
	if codex == nil || codex.Error != "" || !codex.Available {
		service("CODEX", py+194)
		msg := "unavailable"
		if codex != nil && codex.Error != "" {
			msg = codex.Error
		}
		kv("STATUS", msg, py+210)
	} else {
		label := "CODEX"
		if codex.Plan != "" {
			label += "  " + strings.ToUpper(codex.Plan)
		}
		service(label, py+194)
		if codex.Primary != nil {
			kv("PRIMARY "+windowSpan(codex.Primary), codexState(codex.Primary), py+210)
		}
		if codex.Secondary != nil {
			kv("SECONDARY "+windowSpan(codex.Secondary), codexState(codex.Secondary), py+232)
		}
		if codex.Credits != "" {
			kv("CREDITS", codex.Credits+" credits", py+254)
		}
	}

	service("ANTIGRAVITY", py+280)
	anti := m.data.Anti
	if anti == nil || anti.Error != "" || !anti.Available {
		kv("STATUS", "not running", py+298)
	} else {
		// The popup has room for five rows; an overflow indicator keeps the
		// remaining groups from being painted over the card border.
		const maxGroups = 5
		for i, g := range anti.Groups {
			if i >= maxGroups {
				break
			}
			kv(antiGroupName(g.Name), antiGroupValue(g), py+298+float32(i)*20)
		}
		if len(anti.Groups) > maxGroups {
			kv("MORE", "+"+itoa(len(anti.Groups)-maxGroups)+" groups", py+298+float32(maxGroups)*20)
		}
	}
}

// antiGroupName shortens an Antigravity group name for the compact popup.
func antiGroupName(name string) string {
	switch {
	case strings.Contains(name, "Gemini 4"):
		return "ARGON"
	case strings.Contains(name, "Claude") && strings.Contains(name, "GPT"):
		return "GPT+CLAUDE"
	case strings.Contains(name, "Gemini"):
		return "GEMINI"
	}
	// Truncate runes, not bytes, so a Chinese name cannot be cut mid-rune.
	runes := []rune(name)
	if len(runes) > 11 {
		runes = runes[:11]
	}
	return strings.ToUpper(string(runes))
}

// antiGroupValue renders a group's buckets as e.g. "WEEK 99.9%  5H 100%".
func antiGroupValue(g AntiGroup) string {
	parts := make([]string, 0, len(g.Buckets))
	for _, b := range g.Buckets {
		pct := int(math.Round(b.Remaining * 100))
		parts = append(parts, b.Label+" "+itoa(pct)+"%")
	}
	return strings.Join(parts, "  ")
}

// windowSpan turns a limit window into a short "5H" / "7D" label.
func windowSpan(w *CodexWindow) string {
	if w.Seconds <= 0 {
		return ""
	}
	h := w.Seconds / 3600
	label := "(" + itoa(int(h)) + "H)"
	if h >= 24 && h%24 == 0 {
		label = "(" + itoa(int(h/24)) + "D)"
	}
	return label
}

// codexState shows how much of a window remains and when it resets.
func codexState(w *CodexWindow) string {
	return itoa(int(math.Round(100-w.UsedPercent))) + "% left · " + resetIn(w.ResetAt)
}

func resetIn(resetAt int64) string {
	d := resetAt - time.Now().Unix()
	if d <= 0 {
		return "now"
	}
	if d >= 86400 {
		return itoa(int(d/86400)) + "d" + itoa(int((d%86400)/3600)) + "h"
	}
	if d >= 3600 {
		return itoa(int(d/3600)) + "h" + itoa(int((d%3600)/60)) + "m"
	}
	return itoa(int(d/60)) + "m"
}

func itoa(n int) string { return strconv.Itoa(n) }

func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

// fmtFloat1 rounds to a single decimal place (used for the temperature).
func fmtFloat1(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
