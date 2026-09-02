//go:build windows

package buildapp

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Windows user32 封装：OS 级真实鼠标点击。
// BiDi input.performActions 产生的输入不建立 user activation（已证 403），
// 只有 OS 层真实鼠标事件能让 Google Build App 的 bootstrapChannel 放行。
var (
	user32                = syscall.NewLazyDLL("user32.dll")
	kernel32              = syscall.NewLazyDLL("kernel32.dll")
	procEnumWindows       = user32.NewProc("EnumWindows")
	procGetWindowTextW    = user32.NewProc("GetWindowTextW")
	procIsWindowVisible   = user32.NewProc("IsWindowVisible")
	procGetClientRect     = user32.NewProc("GetClientRect")
	procClientToScreen    = user32.NewProc("ClientToScreen")
	procSetCursorPos      = user32.NewProc("SetCursorPos")
	procMouseEvent        = user32.NewProc("mouse_event")
	procSetForeground     = user32.NewProc("SetForegroundWindow")
	procGetForeground     = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadID = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput = user32.NewProc("AttachThreadInput")
	procBringWindowToTop  = user32.NewProc("BringWindowToTop")
	procShowWindow        = user32.NewProc("ShowWindow")
	procKeybdEvent        = user32.NewProc("keybd_event")
	procGetCurrentThread  = kernel32.NewProc("GetCurrentThreadId")
)

type winRect struct{ Left, Top, Right, Bottom int32 }
type winPoint struct{ X, Y int32 }

// findCamoufoxWindow 枚举顶层窗口，返回标题含 "Camoufox" 的可见主窗口句柄。
// 注意：Camoufox（Firefox）主窗口属于子进程而非启动进程，按 PID 匹配会失败，
// 必须按标题匹配（标题形如 "Remix ... | Google AI Studio ‐ Camoufox"）。
func findCamoufoxWindow() (uintptr, error) {
	var found uintptr
	var titleBuf [256]uint16
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			return 1 // continue
		}
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))
		if n == 0 {
			return 1 // continue
		}
		title := syscall.UTF16ToString(titleBuf[:n])
		if len(title) > 0 && (strings.Contains(title, "Camoufox") || strings.Contains(title, "Google AI Studio")) {
			found = hwnd
			return 0 // stop
		}
		return 1 // continue
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	if found == 0 {
		return 0, fmt.Errorf("找不到标题含 Camoufox/Google AI Studio 的可见窗口")
	}
	return found, nil
}

// clientOrigin 返回窗口客户区左上角的屏幕坐标（GetClientRect + ClientToScreen）。
func clientOrigin(hwnd uintptr) (int, int, error) {
	var rect winRect
	if ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return 0, 0, fmt.Errorf("GetClientRect 失败 hwnd=%d", hwnd)
	}
	var pt winPoint
	if ok, _, _ := procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return 0, 0, fmt.Errorf("ClientToScreen 失败 hwnd=%d", hwnd)
	}
	return int(pt.X), int(pt.Y), nil
}

// bringToForeground 将窗口置顶到前台，返回是否真正取得前台焦点。
// 非前台进程直接 SetForegroundWindow 会被 Windows 前台锁拒绝（返回 FALSE）；
// 用 AttachThreadInput 挂接到前台线程后置顶（不用 keybd_event Alt hack，
// 它会向 Firefox 发送 Alt 键事件干扰页面/菜单）。
func bringToForeground(hwnd uintptr) bool {
	const swRestore = 9
	procShowWindow.Call(hwnd, swRestore)
	fg, _, _ := procGetForeground.Call()
	var fgThread uint32
	if fg != 0 {
		procGetWindowThreadID.Call(fg, uintptr(unsafe.Pointer(&fgThread)))
	}
	curThread, _, _ := procGetCurrentThread.Call()
	if fgThread != 0 && uint32(curThread) != fgThread {
		procAttachThreadInput.Call(uintptr(curThread), uintptr(fgThread), 1)
		defer procAttachThreadInput.Call(uintptr(curThread), uintptr(fgThread), 0)
	}
	procBringWindowToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
	fg2, _, _ := procGetForeground.Call()
	return fg2 == hwnd
}

// osClick 在 Camoufox 窗口的客户区视口坐标 (viewportX, viewportY) 处执行真实左键点击。
// 点击前置顶窗口，确保鼠标事件落在 Camoufox 上（OS 级输入直接作用于前台窗口）。
func osClick(pid int, viewportX, viewportY int) error {
	hwnd, err := findCamoufoxWindow()
	if err != nil {
		return err
	}
	bringToForeground(hwnd)
	originX, originY, err := clientOrigin(hwnd)
	if err != nil {
		return err
	}
	screenX := originX + viewportX
	screenY := originY + viewportY
	if ok, _, _ := procSetCursorPos.Call(uintptr(screenX), uintptr(screenY)); ok == 0 {
		return fmt.Errorf("SetCursorPos(%d,%d) 失败", screenX, screenY)
	}
	const (
		mouseEventLeftDown = 0x0002
		mouseEventLeftUp   = 0x0004
	)
	// 对齐 os-click.ps1（已实证 200）的节奏：移入-80ms-按下-50ms-抬起，
	// 过快的 down/up（0ms）可能被 Firefox 事件队列合并为异常输入。
	time.Sleep(80 * time.Millisecond)
	procMouseEvent.Call(mouseEventLeftDown, 0, 0, 0, 0)
	time.Sleep(50 * time.Millisecond)
	procMouseEvent.Call(mouseEventLeftUp, 0, 0, 0, 0)
	// 诊断：打印实际落点、前台状态，便于与人工/ps1 点击比对。
	fg, _, _ := procGetForeground.Call()
	fmt.Fprintf(os.Stderr, "[osclick] click screen=(%d,%d) hwnd=%x fg=%x origin=(%d,%d)\n", screenX, screenY, hwnd, fg, originX, originY)
	return nil
}
