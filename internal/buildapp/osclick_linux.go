//go:build linux

package buildapp

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Linux OS 级真实鼠标点击：依赖 xdotool（X11 XTest 扩展）与 Xvfb 虚拟屏。
// xdotool 的 XTest 事件在 X11 输入层产生，Firefox 视为真实用户手势
// （等价 Windows user32 mouse_event），可建立 user activation。
// 生产部署：容器内 apt-get install -y xvfb xdotool，入口 `Xvfb :99 & export DISPLAY=:99`。

// osClick 在 Camoufox 主进程 pid 的浏览器窗口内，以窗口相对视口坐标 (viewportX, viewportY)
// 执行真实左键点击。`xdotool mousemove --window` 使用窗口相对坐标，
// 浏览器（无 WM 时无装饰）会自行处理边框偏移。
func osClick(pid int, viewportX, viewportY int) error {
	ids, err := xdotool("search", "--pid", strconv.Itoa(pid))
	if err != nil {
		return fmt.Errorf("xdotool search --pid %d: %w", pid, err)
	}
	lines := strings.Fields(ids)
	if len(lines) == 0 {
		return fmt.Errorf("xdotool 未找到 PID %d 的窗口", pid)
	}
	windowID := lines[0]
	out, err := xdotool("mousemove", "--window", windowID,
		strconv.Itoa(viewportX), strconv.Itoa(viewportY), "click", "1")
	if err != nil {
		return fmt.Errorf("xdotool click: %w (%s)", err, out)
	}
	return nil
}

func xdotool(args ...string) (string, error) {
	out, err := exec.Command("xdotool", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(ee.Stderr), err
		}
		return "", err
	}
	return string(out), nil
}
