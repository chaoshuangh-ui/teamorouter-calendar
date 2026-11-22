# TeamoRouter Calendar

一个常驻桌面的 Windows 小组件，用 GOUI 构建，把 **TeamoRouter 用量月历**、**天气**、**工作时间统计**和**备忘录**放在一个透明圆角窗口里。

## 功能

- **月历**：6 个月视图，周一起始，按当天成本着色；点击日期弹出当天用量和三个服务的额度详情。支持翻月。
- **天气**：自动 IP 定位，显示时间、天气、湿度和温度（1 位小数），数据来自 Open-Meteo。
- **工作时间**：鼠标/键盘 10 分钟无输入或关机视为暂停；按两小时分段统计，保留一周趋势折线图和当天时长弹窗。
- **备忘录**：添加、勾选、删除任务；未完成任务会自动顺延到第二天。
- **窗口行为**：无边框、四角透明、不置顶、不抢焦点、不显示任务栏；可拖动，可锁定位置；拖动/锁定/缩放状态持久化。
- **缩放**：顶栏 `− / +` 按钮或 `Ctrl + 滚轮` 等比缩放整个窗口（含圆角外框），点击百分比复位到 100%。范围 70%–150%。

## 构建

需要 Go 1.21+ 和 Windows 环境。

```powershell
$env:PATH="C:\Users\32517\Go\bin;$env:PATH"
$env:GOPROXY="https://goproxy.cn"
$env:GOSUMDB="off"
go build -ldflags="-H=windowsgui" -o TeamoRouterCalendar.exe .
```

`-H=windowsgui` 用来生成 GUI 子系统程序，避免弹出控制台窗口。

测试：

```powershell
go test ./...
go vet ./...
```

## 配置

- **API Key**：只从本机文件读取，不写入仓库、不打印明文：
  ```
  C:\Users\32517\.config\opencode\.secrets\teamorouter-key
  ```
- **面板数据**：`%APPDATA%\TeamoRouterCalendar\panel.json`
- **窗口设置**：`%APPDATA%\TeamoRouterCalendar\settings.json`

## 使用

- 顶栏 `CAL / PNL` 切换日历和面板页。
- 顶栏 `− / +` 缩放，点击中间的百分比复位。
- 锁图标开启/关闭位置锁定；未锁定时拖动顶栏移动窗口。
- 面板页用鼠标滚轮滚动内容；`Ctrl + 滚轮` 缩放整个窗口。
- 备忘录输入框为独立临时窗口，用于保证中文输入法可用。

## 许可证

MIT
