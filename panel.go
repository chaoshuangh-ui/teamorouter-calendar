package main

import (
	"fmt"
	"image/color"
	"sort"
	"time"

	"github.com/golang-gui/goui/core/geometry"
	"github.com/golang-gui/goui/gui"
	"github.com/golang-gui/goui/platform/events"
	"github.com/golang-gui/goui/platform/graphics"
	"github.com/golang-gui/goui/platform/typography"
	"github.com/golang-gui/goui/platform/windows/sdk/winapi"
)

// ---- panel hit actions (kept separate from calendar's hit* constants) ----
const (
	hitTabCal      = 20
	hitTabPanel    = 21
	hitTodoInput   = 22
	hitWorkPoint   = 600 // 600..606 = the seven chart points
	hitTodoToggle  = 700 // 700+i
	hitTodoDel     = 900 // 900+i
	workPointCount = 7
)

// ---- panel layout constants (design space) ----
const (
	// Two 52-wide tabs with an 8 gap span 112, centred in the 400-wide
	// window: (400-112)/2 = 144.
	tabCalX, tabPanelX float32 = 144, 204
	tabY, tabW, tabH   float32 = 20, 52, 20

	// The scrollable content region sits between the fixed header and the
	// fixed "add task" input at the bottom.
	panelContentTop    float32 = 100
	panelContentBottom float32 = 498
	panelViewH         float32 = panelContentBottom - panelContentTop
	panelScrollStep    float32 = 40

	weatherCardX, weatherCardY float32 = 28, 112
	weatherCardW, weatherCardH float32 = 344, 68

	workCardX, workCardY float32 = 28, 202
	workCardW, workCardH float32 = 344, 136

	todoCardX, todoCardY float32 = 28, 356
	todoCardW            float32 = 344
	todoMinCardH         float32 = 142

	todoRowH   float32 = 20
	todoRowTop float32 = 360
	todoCheckX float32 = 46
	todoDelX   float32 = 356

	// A slim scrollbar in the right margin is a passive position indicator;
	// scrolling itself is done with the mouse wheel.
	scrollBarX      float32 = 383 // thumb centre
	scrollTrackW    float32 = 3
	scrollThumbW    float32 = 8
	scrollThumbMinH float32 = 28

	todoInputX, todoInputY float32 = 28, 502
	todoInputW, todoInputH float32 = 344, 16
)

func todoRowCenterY(i int) float32 { return todoRowTop + float32(i)*todoRowH + todoRowH/2 }

// todoCardHeight grows with the number of tasks so the whole list can be
// revealed by scrolling the page, instead of being clipped to six rows.
func todoCardHeight(n int) float32 {
	h := float32(22 + n*20)
	if h < todoMinCardH {
		h = todoMinCardH
	}
	return h
}

// clampPanelScroll keeps the scroll offset inside the content that currently
// exists. Deleting tasks shrinks the content and can otherwise leave the panel
// translated far past its real bottom.
func (w *calendarWidget) clampPanelScroll() {
	max := w.maxPanelScroll()
	if w.panelScroll < 0 {
		w.panelScroll = 0
	} else if w.panelScroll > max {
		w.panelScroll = max
	}
}

// panelHitTest resolves clicks on the daily panel page. Content coordinates
// are shifted back by the current scroll offset before hit-testing.
func (w *calendarWidget) panelHitTest(p geometry.Point) int {
	w.clampPanelScroll()
	cp := geometry.Point{X: p.X, Y: p.Y + w.panelScroll}
	m := w.model

	// Chart points: a small hover/click target around each plotted value.
	for i := 0; i < workPointCount; i++ {
		if pointInCircle(cp, w.workPoints[i].X, w.workPoints[i].Y, 9) {
			return hitWorkPoint + i
		}
	}

	today := time.Now().Format("2006-01-02")
	items := m.panel.todayTodos(today)
	for i := range items {
		cy := todoRowCenterY(i)
		if pointInCircle(cp, todoCheckX, cy, 9) {
			return hitTodoToggle + i
		}
		if pointInCircle(cp, todoDelX, cy, 9) {
			return hitTodoDel + i
		}
	}
	return hitNone
}

// maxPanelScroll is the distance needed to reveal the bottom of the content.
func (w *calendarWidget) maxPanelScroll() float32 {
	today := time.Now().Format("2006-01-02")
	n := len(w.model.panel.todayTodos(today))
	max := todoCardY + todoCardHeight(n) - panelContentBottom
	if max < 0 {
		return 0
	}
	return max
}

// panelContentHeight is the full height of the scrollable content surface.
func (w *calendarWidget) panelContentHeight() float32 {
	today := time.Now().Format("2006-01-02")
	n := len(w.model.panel.todayTodos(today))
	h := todoCardY + todoCardHeight(n) - panelContentTop
	if h < panelViewH {
		return panelViewH
	}
	return h
}

// scrollThumbRect returns the thumb in design space. A zero-width rectangle
// means there is nothing to scroll, so the scrollbar is hidden entirely.
func (w *calendarWidget) scrollThumbRect() geometry.Rectangle {
	w.clampPanelScroll()
	max := w.maxPanelScroll()
	if max <= 0 {
		return geometry.Rectangle{}
	}
	thumbH := panelViewH * panelViewH / w.panelContentHeight()
	if thumbH < scrollThumbMinH {
		thumbH = scrollThumbMinH
	}
	travel := panelViewH - thumbH
	y := panelContentTop
	if travel > 0 {
		y += w.panelScroll / max * travel
	}
	return geometry.Rect(scrollBarX-scrollThumbW/2, y, scrollThumbW, thumbH)
}

// paintPanel draws the whole daily panel page. The header and input bar are
// fixed inside the scaled canvas; everything in between scrolls vertically.
func (w *calendarWidget) paintPanel(p gui.Painter) {
	m := w.model
	w.clampPanelScroll()
	w.paintPanelHeader(p)

	weather, sec, hist, buckets, _, idle := m.tracker.snapshot()
	w.workBuckets = buckets

	// SetClipRect works in widget-local DIP, while painting uses the design
	// space. The widget is resized with zoom, so scaleK already includes it.
	p.Save()
	p.SetClipRect(geometry.Rect(0, panelContentTop*w.scaleK, winW*w.scaleK, panelViewH*w.scaleK))
	p.SetTransform(geometry.Scale(w.scaleK, w.scaleK).Translate(0, -w.panelScroll))
	w.paintWeather(p, weather)
	w.paintWork(p, sec, hist, idle)
	w.paintTodos(p)
	p.Restore()

	// Fixed layers drawn after the clipped content.
	w.paintTodoInput(p)
	w.paintScrollControls(p)
	w.paintWorkTooltip(p)
}

func (w *calendarWidget) paintPanelHeader(p gui.Painter) {
	p.FillEllipse(geometry.Point{X: contentX + 4, Y: 34}, 3.5, 3.5, graphics.RGB(139, 124, 255))
	text(p, "DESK", contentX+16, 24, 80, 20, 9,
		typography.FontWeightSemibold, graphics.RGB(126, 135, 155), typography.TextAlignBegin)

	now := time.Now()
	dateLabel := itoa(int(now.Month())) + "月" + itoa(now.Day()) + "日  周" + shortWeekday(now)
	text(p, dateLabel, contentX, 44, 280, 32, 21,
		typography.FontWeightSemibold, graphics.RGB(242, 244, 248), typography.TextAlignBegin)

	w.paintTabs(p)
	w.paintLock(p)
	w.paintZoomControls(p)
	p.DrawLine(geometry.Point{X: contentX, Y: 90}, geometry.Point{X: 372, Y: 90}, 1, graphics.RGBA(54, 60, 78, 255))
}

// paintTabs draws the page switcher. Short English labels keep the header
// compact; the tabs are shared by both pages so they are always discoverable.
func (w *calendarWidget) paintTabs(p gui.Painter) {
	m := w.model
	for _, b := range []struct {
		x     float32
		label string
		page  int
	}{
		{tabCalX, "CAL", 0},
		{tabPanelX, "PNL", 1},
	} {
		active := m.page == b.page
		bg := graphics.RGBA(38, 43, 57, 255)
		fg := graphics.RGB(150, 160, 182)
		if active {
			bg = graphics.RGBA(96, 78, 176, 255)
			fg = graphics.RGB(245, 242, 255)
		} else if (b.page == 0 && w.hover == hitTabCal) || (b.page == 1 && w.hover == hitTabPanel) {
			bg = graphics.RGBA(56, 62, 82, 255)
		}
		p.FillRoundRect(geometry.Rect(b.x, tabY, tabW, tabH), 8, bg)
		text(p, b.label, b.x, tabY, tabW, tabH, 10,
			typography.FontWeightSemibold, fg, typography.TextAlignCenter)
	}
}

// paintLock is the shared lock toggle, also used by the calendar header.
// Body and shackle are laid out symmetrically around the circle centre.
func (w *calendarWidget) paintLock(p gui.Painter) {
	m := w.model
	lockBg := graphics.RGBA(38, 43, 57, 255)
	if w.hover == hitLock {
		lockBg = graphics.RGBA(56, 62, 82, 255)
	}
	if m.locked {
		lockBg = graphics.RGBA(78, 60, 120, 255)
	}
	p.FillEllipse(geometry.Point{X: lockX, Y: lockY}, lockR, lockR, lockBg)
	lockCol := graphics.RGB(160, 168, 186)
	if m.locked {
		lockCol = graphics.RGB(226, 216, 255)
	}
	sw := float32(1.6)
	if m.locked {
		p.DrawLine(geometry.Point{X: lockX - 3, Y: lockY - 2}, geometry.Point{X: lockX - 3, Y: lockY - 6}, sw, lockCol)
		p.DrawLine(geometry.Point{X: lockX - 3, Y: lockY - 6}, geometry.Point{X: lockX + 3, Y: lockY - 6}, sw, lockCol)
		p.DrawLine(geometry.Point{X: lockX + 3, Y: lockY - 6}, geometry.Point{X: lockX + 3, Y: lockY - 2}, sw, lockCol)
	} else {
		p.DrawLine(geometry.Point{X: lockX - 3, Y: lockY - 2}, geometry.Point{X: lockX - 3, Y: lockY - 6}, sw, lockCol)
		p.DrawLine(geometry.Point{X: lockX - 3, Y: lockY - 6}, geometry.Point{X: lockX + 2, Y: lockY - 6}, sw, lockCol)
	}
	p.FillRoundRect(geometry.Rect(lockX-5, lockY-2, 10, 8), 2, lockCol)
}

func panelSection(p gui.Painter, label string, y float32, right string) {
	text(p, label, contentX, y, 200, 14, 9,
		typography.FontWeightSemibold, graphics.RGB(139, 124, 255), typography.TextAlignBegin)
	if right != "" {
		text(p, right, 200, y, 172, 14, 9,
			typography.FontWeightRegular, graphics.RGB(120, 130, 150), typography.TextAlignEnd)
	}
}

func (w *calendarWidget) paintWeather(p gui.Painter, wt weatherData) {
	panelSection(p, "WEATHER", 100, wt.City)
	p.FillRoundRect(geometry.Rect(weatherCardX, weatherCardY, weatherCardW, weatherCardH), 16, graphics.RGBA(27, 31, 42, 255))

	if !wt.Available {
		text(p, "天气获取中...", weatherCardX+16, weatherCardY, weatherCardW-32, weatherCardH, 13,
			typography.FontWeightRegular, graphics.RGB(120, 130, 150), typography.TextAlignCenter)
		return
	}

	timeLabel := wt.Time
	if timeLabel == "" {
		timeLabel = time.Now().Format("15:04")
	}
	text(p, timeLabel, weatherCardX+16, weatherCardY+10, 150, 40, 29,
		typography.FontWeightSemibold, graphics.RGB(244, 246, 252), typography.TextAlignBegin)

	cond := wt.Desc
	if wt.Temp != 0 {
		cond += "  " + fmtFloat1(wt.Temp) + "°C"
	}
	text(p, cond, weatherCardX+168, weatherCardY+8, weatherCardW-184, 24, 14,
		typography.FontWeightSemibold, graphics.RGB(214, 220, 236), typography.TextAlignEnd)
	text(p, "湿度 "+itoa(wt.Humidity)+"%", weatherCardX+168, weatherCardY+36, weatherCardW-184, 20, 11,
		typography.FontWeightRegular, graphics.RGB(150, 160, 182), typography.TextAlignEnd)
}

// paintWork shows today's total plus a 7-day line chart. The plotted points
// are cached so hover hit-testing and the tooltip use identical coordinates.
func (w *calendarWidget) paintWork(p gui.Painter, sec int64, hist map[string]int64, idle bool) {
	panelSection(p, "WORK TIME", 194, "")

	p.FillRoundRect(geometry.Rect(workCardX, workCardY, workCardW, workCardH), 16, graphics.RGBA(27, 31, 42, 255))

	text(p, "今日工作", workCardX+16, workCardY+6, 120, 16, 9,
		typography.FontWeightRegular, graphics.RGB(120, 130, 150), typography.TextAlignBegin)
	text(p, formatDuration(sec), workCardX+16, workCardY+20, 200, 36, 25,
		typography.FontWeightSemibold, graphics.RGB(244, 246, 252), typography.TextAlignBegin)

	running := !idle
	pillBg := graphics.RGBA(40, 92, 72, 255)
	pillFg := graphics.RGB(150, 226, 190)
	pillLabel := "计时中"
	if !running {
		pillBg = graphics.RGBA(92, 68, 40, 255)
		pillFg = graphics.RGB(232, 194, 140)
		pillLabel = "已暂停"
	}
	p.FillRoundRect(geometry.Rect(workCardX+workCardW-104, workCardY+22, 88, 24), 12, pillBg)
	text(p, pillLabel, workCardX+workCardW-104, workCardY+22, 88, 24, 10,
		typography.FontWeightSemibold, pillFg, typography.TextAlignCenter)

	text(p, "近 7 天 · 小时", workCardX+16, workCardY+60, 200, 14, 9,
		typography.FontWeightRegular, graphics.RGB(120, 130, 150), typography.TextAlignBegin)

	chartLeft := workCardX + 22
	chartRight := workCardX + workCardW - 22
	chartTop := workCardY + 80
	chartBottom := workCardY + workCardH - 20

	for gi := 0; gi < 3; gi++ {
		gy := chartTop + float32(gi)*(chartBottom-chartTop)/2
		p.DrawLine(geometry.Point{X: chartLeft, Y: gy}, geometry.Point{X: chartRight, Y: gy}, 1, graphics.RGBA(46, 52, 68, 255))
	}

	now := time.Now()
	todayKey := now.Format("2006-01-02")
	maxSec := int64(1)
	for i := 0; i < workPointCount; i++ {
		key := now.AddDate(0, 0, i-6).Format("2006-01-02")
		if key == todayKey {
			w.workDays[i] = sec
		} else {
			w.workDays[i] = hist[key]
		}
		if w.workDays[i] > maxSec {
			maxSec = w.workDays[i]
		}
	}

	span := chartRight - chartLeft
	for i := 0; i < workPointCount; i++ {
		x := chartLeft + span*float32(i)/6
		y := chartBottom - float32(w.workDays[i])/float32(maxSec)*(chartBottom-chartTop)
		if w.workDays[i] <= 0 {
			y = chartBottom
		}
		w.workPoints[i] = geometry.Point{X: x, Y: y}
	}

	lineCol := graphics.RGB(139, 124, 255)
	for i := 0; i < workPointCount-1; i++ {
		p.DrawLine(w.workPoints[i], w.workPoints[i+1], 2, lineCol)
	}
	for i := 0; i < workPointCount; i++ {
		r := float32(3)
		dotCol := graphics.RGB(139, 124, 255)
		if i == workPointCount-1 {
			r = 4
			dotCol = graphics.RGB(226, 216, 255)
		}
		if w.hover == hitWorkPoint+i {
			r += 2
			dotCol = graphics.RGB(255, 255, 255)
		}
		p.FillEllipse(w.workPoints[i], r, r, dotCol)
	}

	for i := 0; i < workPointCount; i++ {
		d := now.AddDate(0, 0, i-6)
		x := chartLeft + span*float32(i)/6 - 10
		text(p, shortWeekday(d), x, chartBottom+1, 20, 14, 9,
			typography.FontWeightRegular, colorRGB(i == workPointCount-1, 180, 170, 255, 110, 120, 142), typography.TextAlignCenter)
	}
}

// paintWorkTooltip floats above the hovered chart point. It shows the day's
// date and total, then breaks the total into fixed two-hour clock buckets.
// Old days without bucket data only show the stored total.
func (w *calendarWidget) paintWorkTooltip(p gui.Painter) {
	idx := w.workTipIdx
	if idx < 0 {
		for i := 0; i < workPointCount; i++ {
			if w.hover == hitWorkPoint+i {
				idx = i
				break
			}
		}
	}
	if idx < 0 || idx >= workPointCount {
		return
	}

	d := time.Now().AddDate(0, 0, idx-6)
	key := d.Format("2006-01-02")
	dateLabel := itoa(int(d.Month())) + "月" + itoa(d.Day()) + "日  周" + shortWeekday(d)
	totalLabel := "总计  " + formatDuration(w.workDays[idx])
	buckets := w.workBuckets[key]

	const (
		tipW      float32 = 250
		tipPad    float32 = 10
		titleH    float32 = 18
		totalH    float32 = 20
		rowH      float32 = 16
		tipCorner float32 = 8
	)
	type bucketRow struct {
		idx int
		sec int64
	}
	var rowsData []bucketRow
	for i, sec := range buckets {
		if sec > 0 {
			rowsData = append(rowsData, bucketRow{idx: i, sec: sec})
		}
	}
	// Map iteration is random; keep the fixed clock buckets in time order.
	sort.Slice(rowsData, func(i, j int) bool { return rowsData[i].idx < rowsData[j].idx })
	rows := len(rowsData)
	if rows == 0 {
		rows = 1
	}
	bh := 2*tipPad + titleH + totalH + float32(rows)*rowH

	pt := w.workPoints[idx]
	bx := pt.X - tipW/2
	by := pt.Y - w.panelScroll - bh - 12
	if bx < 4 {
		bx = 4
	}
	if bx+tipW > winW-4 {
		bx = winW - 4 - tipW
	}
	if by < panelContentTop+4 {
		by = pt.Y - w.panelScroll + 14
	}
	if by < panelContentTop+4 {
		by = panelContentTop + 4
	}
	if by+bh > panelContentBottom-4 {
		by = panelContentBottom - 4 - bh
	}

	box := geometry.Rect(bx, by, tipW, bh)
	p.FillRoundRect(box, tipCorner, graphics.RGBA(14, 17, 24, 255))
	p.DrawRoundRect(box, tipCorner, 1, graphics.RGB(139, 124, 255))

	text(p, dateLabel, bx+tipPad, by+tipPad, tipW-2*tipPad, titleH, 9,
		typography.FontWeightRegular, graphics.RGB(150, 160, 182), typography.TextAlignBegin)
	text(p, totalLabel, bx+tipPad, by+tipPad+titleH, tipW-2*tipPad, totalH, 12,
		typography.FontWeightSemibold, graphics.RGB(240, 238, 255), typography.TextAlignBegin)

	rowY := by + tipPad + titleH + totalH
	if len(rowsData) == 0 {
		text(p, "暂无工作时长", bx+tipPad, rowY, tipW-2*tipPad, rowH, 9,
			typography.FontWeightRegular, graphics.RGB(110, 120, 142), typography.TextAlignBegin)
		return
	}
	for _, r := range rowsData {
		label := fmt.Sprintf("%02d:00-%02d:00", r.idx*bucketHours, (r.idx+1)*bucketHours)
		text(p, label, bx+tipPad, rowY, 110, rowH, 9,
			typography.FontWeightRegular, graphics.RGB(150, 160, 182), typography.TextAlignBegin)
		text(p, formatDuration(r.sec), bx+122, rowY, tipW-132, rowH, 10,
			typography.FontWeightSemibold, graphics.RGB(226, 216, 255), typography.TextAlignEnd)
		rowY += rowH
	}
}

// paintScrollControls draws a slim right-edge rail and thumb. It is a passive
// position indicator only: the mouse wheel performs the actual scrolling.
func (w *calendarWidget) paintScrollControls(p gui.Painter) {
	rail := w.scrollThumbRect()
	if rail.Width <= 0 {
		return
	}
	p.FillRoundRect(geometry.Rect(scrollBarX-scrollTrackW/2, panelContentTop, scrollTrackW, panelViewH), 2,
		graphics.RGBA(48, 54, 70, 255))
	p.FillRoundRect(rail, 4, graphics.RGB(104, 96, 142))
}

func (w *calendarWidget) paintTodos(p gui.Painter) {
	panelSection(p, "TODO", 344, "")

	today := time.Now().Format("2006-01-02")
	items := w.model.panel.todayTodos(today)
	h := todoCardHeight(len(items))
	p.FillRoundRect(geometry.Rect(todoCardX, todoCardY, todoCardW, h), 16, graphics.RGBA(27, 31, 42, 255))

	if len(items) == 0 {
		text(p, "还没有任务，点击下面添加", todoCardX+16, todoCardY+24, todoCardW-32, h-48, 12,
			typography.FontWeightRegular, graphics.RGB(110, 120, 142), typography.TextAlignCenter)
	}

	for i, it := range items {
		cy := todoRowCenterY(i)

		boxBg := graphics.RGBA(44, 50, 66, 255)
		boxEdge := graphics.RGB(110, 120, 142)
		if it.Done {
			boxBg = graphics.RGBA(96, 78, 176, 255)
			boxEdge = graphics.RGB(180, 170, 255)
		} else if w.hover == hitTodoToggle+i {
			boxBg = graphics.RGBA(60, 66, 88, 255)
		}
		p.FillEllipse(geometry.Point{X: todoCheckX, Y: cy}, 8, 8, boxBg)
		p.DrawEllipse(geometry.Point{X: todoCheckX, Y: cy}, 8, 8, 1, boxEdge)
		if it.Done {
			p.DrawLine(geometry.Point{X: todoCheckX - 3, Y: cy}, geometry.Point{X: todoCheckX - 1, Y: cy + 3}, 1.8, graphics.RGB(245, 242, 255))
			p.DrawLine(geometry.Point{X: todoCheckX - 1, Y: cy + 3}, geometry.Point{X: todoCheckX + 4, Y: cy - 3}, 1.8, graphics.RGB(245, 242, 255))
		}

		fg := graphics.RGB(228, 233, 244)
		if it.Done {
			fg = graphics.RGB(120, 130, 150)
		}
		text(p, it.Text, todoCheckX+18, todoRowTop+float32(i)*todoRowH, todoDelX-todoCheckX-40, todoRowH, 11,
			typography.FontWeightRegular, fg, typography.TextAlignBegin)

		delBg := graphics.RGBA(44, 50, 66, 255)
		if w.hover == hitTodoDel+i {
			delBg = graphics.RGBA(120, 58, 66, 255)
		}
		p.FillEllipse(geometry.Point{X: todoDelX, Y: cy}, 9, 9, delBg)
		text(p, "×", todoDelX-9, cy-9, 18, 18, 14,
			typography.FontWeightRegular, graphics.RGB(210, 216, 230), typography.TextAlignCenter)
	}
}

func (w *calendarWidget) paintTodoInput(p gui.Painter) {
	inputBg := graphics.RGBA(34, 39, 52, 255)
	inputEdge := graphics.RGB(70, 76, 98)
	if w.hover == hitTodoInput {
		inputBg = graphics.RGBA(44, 50, 66, 255)
	}
	p.FillRoundRect(geometry.Rect(todoInputX, todoInputY, todoInputW, todoInputH+4), 8, inputBg)
	p.DrawRoundRect(geometry.Rect(todoInputX, todoInputY, todoInputW, todoInputH+4), 8, 1, inputEdge)
	hint := "+  点击输入新任务"
	if w.todoInputActive {
		hint = w.todoInputText
		if hint == "" {
			hint = "输入中..."
		}
	}
	text(p, hint, todoInputX+12, todoInputY, todoInputW-24, todoInputH+4, 11,
		typography.FontWeightRegular, colorRGB(w.todoInputActive, 236, 240, 248, 120, 130, 150), typography.TextAlignBegin)
}

// beginTodoInput opens the standalone, activatable input window. A separate
// window is required because the desktop widget is WS_EX_NOACTIVATE and cannot
// receive IME keyboard messages for Chinese text.
func (w *calendarWidget) beginTodoInput() {
	m := w.model
	w.todoInputActive = true
	w.todoInputText = ""
	m.app.Post(func() {
		if win := m.app.FindWindow("memo-input"); win != nil {
			hwnd := winapi.HWND(win.PlatformWindow().NativeHandle())
			forceForeground(hwnd)
			_ = win.Show()
			forceForeground(hwnd)
			if input := m.app.FindWidget("memo-input", "memo-text"); input != nil {
				win.SetFocusedWidget(input)
			}
		}
	})
}

func colorRGB(cond bool, r1, g1, b1, r2, g2, b2 byte) color.Color {
	if cond {
		return graphics.RGB(r1, g1, b1)
	}
	return graphics.RGB(r2, g2, b2)
}

// todoWheelController lets the mouse wheel scroll the whole panel content.
type todoWheelController struct {
	gui.EventControllerBase
	w *calendarWidget
}

func newTodoWheelController(w *calendarWidget) *todoWheelController {
	return &todoWheelController{
		EventControllerBase: gui.NewEventControllerBase(gui.PhaseBubble),
		w:                   w,
	}
}

func (c *todoWheelController) HandleEvent(ctx gui.EventContext) {
	e, ok := ctx.Event().(events.WheelEvent)
	if !ok {
		return
	}
	w := c.w
	// Ctrl + wheel zooms the whole canvas on either page; plain wheel keeps
	// scrolling the panel on the daily page.
	if e.Modifiers&events.ModifierControl != 0 {
		if e.DeltaY > 0 {
			w.setZoom(w.model.zoom - 0.1)
		} else if e.DeltaY < 0 {
			w.setZoom(w.model.zoom + 0.1)
		}
		ctx.StopPropagation()
		return
	}
	if w.model.page != 1 || w.model.selected >= 0 {
		return
	}
	pos, ok := ctx.Position()
	if !ok {
		return
	}
	dp := w.designPos(pos)
	if !pointInRect(dp, 0, panelContentTop, winW, panelViewH) {
		return
	}
	// GOUI reports wheel motion in lines (one notch is 1). Honour the real
	// delta so a fast wheel or trackpad does not need many events.
	steps := e.DeltaY
	if steps > 5 {
		steps = 5
	} else if steps < -5 {
		steps = -5
	}
	if steps == 0 {
		return
	}
	next := w.panelScroll + steps*panelScrollStep
	max := w.maxPanelScroll()
	if next < 0 {
		next = 0
	}
	if next > max {
		next = max
	}
	if next == w.panelScroll {
		return // at an edge: do not swallow the event
	}
	w.panelScroll = next
	w.hover = hitNone
	w.RequestPaint()
	ctx.StopPropagation()
}
