# TeamoRouter Calendar — 项目交接 / 对话记录

> 用途：把本次对话与项目状态固化成一份记录，方便后续新会话快速「接上」。
> 更新日期：2026-11-23

---

## 1. 项目概况

- **名称**：TeamoRouter Calendar（桌面常驻小组件）
- **技术栈**：Go + GOUI `v0.8.0`，单 EXE，SQLite 读取用 `modernc.org/sqlite`
- **本地路径**：`E:\A.科研生活\04 opencode-home\teamorouter-calendar`
- **GitHub**：https://github.com/chaoshuangh-ui/teamorouter-calendar （公开，分支 `master`）
- **git 身份**：`chaoshuangh-ui` / `chaoshuangh-ui@users.noreply.github.com`
- **gh CLI**：不在 PATH，但实际安装在 `C:\Program Files\GitHub CLI\gh.exe`，已登录 `chaoshuangh-ui`（keyring，token scopes: repo/workflow/gist/read:org）

### Git 提交历史
- `aa6ea69` Initial commit: TeamoRouter desktop calendar widget
- `8459d61` Fix white corners: erase theme window background before drawing rounded card

推送命令：`git push origin master`（PowerShell 会把进度写 stderr 显示成红色报错，实际推送成功，用 `git log`/`git status` 确认即可）。

---

## 2. 已实现功能（全部完成）

- **月历**：6 个月、周一起始、上/下月切换、一天一格、按当天成本着色、格内仅日期号；点击日期弹详情。
- **弹窗额度**：三服务额度；TeamoRouter 余额只显示一次；Codex 显示剩余量；Antigravity 显示一行 `GPT+CLAUDE`。
- **天气**：IP 自动定位（ipapi.co，fallback ipinfo.io）+ Open-Meteo；只显示时间/气候/湿度/温度；温度 1 位小数。
- **工作时间**：10 分钟鼠标/键盘无输入视为暂停，关机也暂停；每天结束保留前一天时长；一周趋势折线图 + 折线点时长小弹窗；按两小时一段，只显示 `sec>0` 的桶，旧无明细日期仍只显示总时长。
- **备忘录**：添加、打勾、删除；未完成任务顺延到第二天；输入用独立临时窗口保证中文输入法可用。
- **窗口行为**：无边框、透明、四角圆弧透明；header 拖动；锁定位置并持久化；桌面底层、不抢焦点、不置顶、无任务栏图标；开机自启。
- **缩放**：顶栏 `− / +` 或 `Ctrl+滚轮`，**整个窗口（含圆角外框）等比缩放**，点击百分比复位 100%；范围 70%–150%；持久化。

---

## 3. 关键架构与决策

- **设计空间** `winW,winH = 400,520`；窗口 `Size(320,416)`；基础 `shrink=0.8`；DPI-aware 下物理窗口 `400x520`（本机 design 坐标 == 物理像素）。
- **整体缩放**：`setZoom()` 调 `winapi.SetWindowPos` 改变物理 HWND 尺寸；`scaleK = Rect().Width / winW` 自动含缩放，绘制与命中共用同一变换；启动时按保存的 zoom 设置初始尺寸。100% 保持原外观。
- **桌面挂载**：窗口设为 `WS_CHILD` 后挂到 `WorkerW`；启动延迟重试 + 每分钟重挂检测（`screenToParent` 做屏幕→父窗口坐标转换）。
- **面板滚动**：天气/工作/任务为连续内容面，在 `panelContentTop=100` 到 `panelContentBottom=498` 可视带内裁剪滚动；顶栏/输入栏固定（在缩放后的画布内）。
- **工作桶**：固定 12 个两小时桶（`bucketHours=2`, `bucketCount=12`）；旧 `segments` 已迁移为 `buckets`。
- **四角透明**（最近修复）：窗口是 `WindowChromeNone`，GOUI 自带 `clearRoundCorners` 只对 integrated chrome 生效，所以主题会把不透明 `#F6F6F8` 铺满客户区，圆角外留下白色楔形。修复：在 `Paint()` 开头用 `p.ClearPath(...)` 把整个客户区擦成透明，再画圆角卡片。

### 配置 / 数据位置
- API Key 只从 `C:\Users\32517\.config\opencode\.secrets\teamorouter-key` 读取，不写配置、不明文打印。
- 面板数据：`%APPDATA%\TeamoRouterCalendar\panel.json`
- 窗口设置：`%APPDATA%\TeamoRouterCalendar\settings.json`
- 当前 settings（100%，无 zoom 字段）：`{"locked":true,"x":2018,"y":-2}`
- **写 settings.json 必须用无 BOM 的 UTF-8**，否则 Go 解析失败并重置位置：
  ```powershell
  [System.IO.File]::WriteAllText($path, '{"locked":true,"x":2018,"y":-2}', (New-Object System.Text.UTF8Encoding($false)))
  ```
  不要用 `Set-Content -Encoding UTF8`（会带 BOM）。

### 已废弃、勿复用
`GOUI_PLAT_SCALE`、`SetWindowRgn`、`HWND_TOPMOST`、`shrink / p.PixelScale()`、agentapi/sidecar 执行链、`TRC_DEBUG` / `trc_*.txt` 等调试埋点。

---

## 4. 构建与校验

```powershell
$env:PATH="C:\Users\32517\Go\bin;$env:PATH"
$env:GOPROXY="https://goproxy.cn"
$env:GOSUMDB="off"
Set-Location "E:\A.科研生活\04 opencode-home\teamorouter-calendar"
go build -ldflags="-H=windowsgui" -o TeamoRouterCalendar.exe .
```

- `-H=windowsgui` → GUI 子系统，无控制台。校验：PE `Magic=523 Subsystem=2`。
- 测试：`go test ./...`、`go vet ./...`、`gofmt -w .`
- EXE 启动慢，约 45–55 秒；无任务栏图标，关闭用任务管理器结束 `TeamoRouterCalendar` 进程。
- 开机自启：启动文件夹 `TeamoRouter Calendar.lnk`；桌面也有同名快捷方式。

---

## 5. 回归脚本（临时目录）

目录：`C:\Users\32517\AppData\Local\Temp\opencode\`

| 脚本 | 用途 |
|---|---|
| `trc_verify2.ps1` | 按 PID 找窗口并截图（推荐，兼容桌面子窗口） |
| `trc_click.ps1` | 按 design 坐标点击并截图 |
| `trc_click_slow.ps1` | 点击后等待 1800ms 再截图 |
| `trc_ctrlwheel.ps1` | 发送 Ctrl+滚轮 |
| `trc_wheel.ps1` / `trc_wheelcheck.ps1` | 面板滚动 |
| `trc_diff.ps1` | 两图区域像素差 |
| `trc_tipcheck.ps1` / `trc_bucketcheck.ps1` | tooltip / 两小时桶 |
| `trc_corner_once.ps1` / `trc_corner_full.ps1` | 四角与整窗白色像素检查 |

**所有截图/点击脚本必须先** `SetProcessDpiAwarenessContext(-4)`，否则物理坐标会被误判为 design 坐标。

缩放对照实测：100% → `400x520`，90% → `360x468`，70% → `280x364`，120% → `480x624`。四角白色修复后：100% 与 90% 下窗口内 `F6F6F8` 像素数均为 0，四角显示壁纸色。

---

## 6. 文件清单

| 文件 | 说明 |
|---|---|
| `calendar.go` | 缩放核心、`Paint()`、命中检测、顶栏、月历、弹窗、缩放按钮 |
| `panel.go` | 面板绘制 / 滚动 / 折线 tooltip / 两小时桶 / 滚轮缩放 |
| `panel_data.go` | 工作 tracker、天气、额度状态（稳定，避免回归） |
| `main.go` | 启动、桌面挂载、刷新/退出生命周期 |
| `settings.go` | `settings.Zoom` 与原子保存 |
| `data.go` / `codex.go` / `antigravity.go` | 三服务额度 API（已加固） |
| `desktop.go` | `screenToParent` / `parentOf` / `WS_CHILD` / WorkerW / 空闲检测 |
| `memo.go` | 临时中文输入窗口 |
| `data_test.go` | 离线单元测试 |
| `README.md` / `LICENSE` / `.gitignore` | 仓库文档（中文 README，MIT，排除 exe） |
| `HANDOFF.md` | 本记录 |

---

## 7. 后续如何「接上」

1. 新会话里先读本文件（或直接说「读 HANDOFF.md」）。
2. 代码在 `E:\A.科研生活\04 opencode-home\teamorouter-calendar`，非 git 子模块，独立仓库。
3. 任何改动后：`gofmt -w .` → `go test ./...` → `go vet ./...` → 重建 EXE → 按第 5 节脚本回归 → `git add/commit/push`。
4. 若改窗口位置/缩放，注意同时更新 `settings.json`（无 BOM）并重启应用验证。
