package camoufoxnative

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ClientWindowRect 返回当前 BiDi 会话关联的可见 client window 的屏幕坐标与尺寸。
// 供 OS 级输入注入（换算视口坐标 -> 屏幕坐标）使用。
func (s *Session) ClientWindowRect(ctx context.Context) (x, y, width, height float64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, 0, 0, 0, errors.New("Camoufox session 已关闭")
	}
	result, err := s.client.command(ctx, "browser.getClientWindows", map[string]any{})
	if err != nil {
		return 0, 0, 0, 0, err
	}
	windows, _ := result["clientWindows"].([]any)
	best := ""
	for _, item := range windows {
		window, _ := item.(map[string]any)
		id, _ := window["clientWindow"].(string)
		if id == "" {
			continue
		}
		active, _ := window["active"].(bool)
		if active || best == "" {
			best = id
		}
	}
	if best == "" {
		return 0, 0, 0, 0, errors.New("Camoufox BiDi 未返回 client window")
	}
	for _, item := range windows {
		window, _ := item.(map[string]any)
		id, _ := window["clientWindow"].(string)
		if id != best {
			continue
		}
		x, _ = window["x"].(float64)
		y, _ = window["y"].(float64)
		width, _ = window["width"].(float64)
		height, _ = window["height"].(float64)
		return x, y, width, height, nil
	}
	return 0, 0, 0, 0, errors.New("Camoufox BiDi client window 未匹配")
}

// RestoreClientWindow 恢复当前 BiDi 会话关联的可见 client window。
// Camoufox 有头窗口可见时，Firefox 输入模块仍可能把窗口视口初始化为 0x0。
func (s *Session) RestoreClientWindow(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}

	result, err := s.client.command(ctx, "browser.getClientWindows", map[string]any{})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(result)
	fmt.Fprintf(os.Stderr, "[camoufox] clientWindows: %s\n", raw)
	windows, _ := result["clientWindows"].([]any)
	clientWindow := ""
	for _, item := range windows {
		window, _ := item.(map[string]any)
		active, _ := window["active"].(bool)
		id, _ := window["clientWindow"].(string)
		if id != "" && (clientWindow == "" || active) {
			clientWindow = id
		}
		if active && id != "" {
			break
		}
	}
	if clientWindow == "" {
		return errors.New("Camoufox BiDi 未返回 client window")
	}
	_, err = s.client.command(ctx, "browser.setClientWindowState", map[string]any{
		"clientWindow": clientWindow,
		"state":        "normal",
	})
	return err
}
