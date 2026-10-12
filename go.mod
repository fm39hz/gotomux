module github.com/fm39hz/gotomux

go 1.26.5

// Cursor-style restoration patch for Bubble Tea upstream issue #1559.
replace charm.land/bubbletea/v2 => github.com/fm39hz/bubbletea/v2 v2.1.1-0.20261012033831-caed214dc9de

require (
	charm.land/bubbles/v2 v2.1.1
	charm.land/bubbletea/v2 v2.1.0
	github.com/BurntSushi/toml v1.6.0
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/epilande/go-devicons v0.0.0-20250505162540-0661cab71a28
	github.com/junegunn/fzf v0.74.1
	github.com/richardwooding/projectdetect v0.7.0
	github.com/shirou/gopsutil/v4 v4.26.6
	golang.org/x/text v0.28.0
	modernc.org/sqlite v1.54.0
)

require (
	charm.land/lipgloss/v2 v2.0.4 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20261008173134-6b8d4baf91b4 // indirect
	github.com/charmbracelet/x/ansi v0.11.9 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/junegunn/go-shellwords v0.0.0-20250127100254-2aa3b3277741 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/mattn/go-runewidth v0.0.30 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/xo/terminfo v1.0.0 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
