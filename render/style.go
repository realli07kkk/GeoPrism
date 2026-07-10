package render

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// 语义化颜色定义，自动适配亮色/暗色终端
var (
	ColorSuccess = lipgloss.AdaptiveColor{Light: "2", Dark: "10"} // 绿色
	ColorError   = lipgloss.AdaptiveColor{Light: "1", Dark: "9"}  // 红色
	ColorTitle   = lipgloss.AdaptiveColor{Light: "4", Dark: "12"} // 蓝色
	ColorMuted   = lipgloss.AdaptiveColor{Light: "8", Dark: "8"}  // 灰色
	ColorAccent  = lipgloss.AdaptiveColor{Light: "6", Dark: "14"} // 青色
)

// 公共样式
var (
	HeaderStyle  = lipgloss.NewStyle().Bold(true).Foreground(ColorTitle)
	SuccessStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
	ErrorStyle   = lipgloss.NewStyle().Foreground(ColorError)
	MutedStyle   = lipgloss.NewStyle().Foreground(ColorMuted)
	AccentStyle  = lipgloss.NewStyle().Foreground(ColorAccent)
)

// isTTY 检测 writer 是否为终端
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
