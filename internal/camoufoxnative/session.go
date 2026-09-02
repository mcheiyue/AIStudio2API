package camoufoxnative

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Session 是通用 Camoufox 浏览器会话，不绑定 Playground WAA bootstrap。
// Build App 线用它启动浏览器、安装 2267 会话、导航到 applet，并在页面（含子帧）上下文执行 JS。
type Session struct {
	mu         sync.Mutex
	process    *browserProcess
	connection *websocket.Conn
	client     *bidiClient
	contextID  string
	closed     bool
}

// StartSession 启动隔离 Camoufox，安装 storage-state 的 cookie/localStorage，但不导航到任何页面。
// 调用方负责用 Navigate 跳到 Build App applet，再用 Evaluate* 在页面上下文执行脚本。
func StartSession(ctx context.Context, options Options) (*Session, error) {
	state, err := loadStorageState(options.StorageStatePath)
	if err != nil {
		return nil, err
	}
	if options.ExecutablePath == "" {
		return nil, errors.New("StartSession 需要 ExecutablePath")
	}
	ffVersion, err := browserMajor(options.ExecutablePath)
	if err != nil {
		return nil, err
	}
	fingerprint, err := loadAccountCamoufoxConfig(options.StorageStatePath, ffVersion, options.Locale, options.Timezone)
	if err != nil {
		return nil, err
	}
	process, endpoint, err := launchBrowser(ctx, options, fingerprint)
	if err != nil {
		return nil, err
	}
	session := &Session{process: process}
	failed := true
	defer func() {
		if failed {
			_ = session.Close()
		}
	}()
	dialer := websocket.Dialer{HandshakeTimeout: 30 * time.Second}
	connection, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("连接 Camoufox BiDi: %w", err)
	}
	session.connection = connection
	session.client = newBiDiClient(connection)
	if _, err := session.client.command(ctx, "session.new", map[string]any{"capabilities": map[string]any{}}); err != nil {
		return nil, err
	}
	tree, err := session.client.command(ctx, "browsingContext.getTree", map[string]any{"maxDepth": 0})
	if err != nil {
		return nil, err
	}
	contexts, _ := tree["contexts"].([]any)
	if len(contexts) == 0 {
		return nil, errors.New("Camoufox BiDi 未返回初始 tab")
	}
	root, _ := contexts[0].(map[string]any)
	contextID, _ := root["context"].(string)
	if contextID == "" {
		return nil, errors.New("Camoufox BiDi 初始 tab 无效")
	}
	session.contextID = contextID
	if err := session.client.installLocalStorage(ctx, contextID, state.Origins); err != nil {
		return nil, err
	}
	if err := session.client.installCookies(ctx, state.Cookies); err != nil {
		return nil, err
	}
	failed = false
	return session, nil
}

// ContextID 返回顶层 browsing context ID。
func (s *Session) ContextID() string { return s.contextID }

// AddInitScript 注册一个在页面（含后续导航）脚本前运行的 preload 脚本（等价于 Playwright addInitScript）。
// 用于注入 authIndex responder 等必须在 applet 加载前就位的监听。functionDeclaration 必须是函数表达式。
func (s *Session) AddInitScript(ctx context.Context, functionDeclaration string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	_, err := s.client.command(ctx, "script.addPreloadScript", map[string]any{
		"functionDeclaration": functionDeclaration,
		"contexts":            []string{s.contextID},
	})
	return err
}

// AddInitScriptAll 注入 preload script 到所有 browsing context（含跨域 iframe），
// 用于需要在子帧（如 run.app）中执行的脚本（如 click capture）。
func (s *Session) AddInitScriptAll(ctx context.Context, functionDeclaration string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	// 不传 contexts 参数 → BiDi 在所有 context 中执行
	_, err := s.client.command(ctx, "script.addPreloadScript", map[string]any{
		"functionDeclaration": functionDeclaration,
	})
	return err
}

// Navigate 在顶层 context 导航到指定 URL。遇到瞬态网络错误（NET_RESET/ABORT）自动重试最多 3 次。
func (s *Session) Navigate(ctx context.Context, rawURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_, err := s.client.command(ctx, "browsingContext.navigate", map[string]any{
			"context": s.contextID,
			"url":     rawURL,
			"wait":    "interactive",
		})
		if err == nil {
			return nil
		}
		msg := err.Error()
		if strings.Contains(msg, "NS_ERROR_ABORT") || strings.Contains(msg, "NS_ERROR_NET_RESET") || strings.Contains(msg, "NS_ERROR_NET_INTERRUPT") {
			lastErr = err
			log.Printf("[camoufox] navigate attempt %d/%d transient error: %v, retrying...", attempt+1, 3, err)
			time.Sleep(time.Duration(3*(attempt+1)) * time.Second)
			continue
		}
		return fmt.Errorf("导航 %s: %w", rawURL, err)
	}
	return fmt.Errorf("导航 %s (3 次重试后): %w", rawURL, lastErr)
}

// Evaluate 在顶层 context 执行表达式（awaitPromise）。
func (s *Session) Evaluate(ctx context.Context, expression string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Camoufox session 已关闭")
	}
	return s.client.evaluate(ctx, s.contextID, expression)
}

// EvaluateString 在顶层 context 执行表达式并返回字符串值。
func (s *Session) EvaluateString(ctx context.Context, expression string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("Camoufox session 已关闭")
	}
	return s.client.evaluateString(ctx, s.contextID, expression)
}

// EvaluateInContext 在指定（子帧）context 执行表达式（awaitPromise）。
func (s *Session) EvaluateInContext(ctx context.Context, contextID, expression string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Camoufox session 已关闭")
	}
	return s.client.evaluate(ctx, contextID, expression)
}

// EvaluateStringInContext 在指定（子帧）context 执行表达式并返回字符串值。
func (s *Session) EvaluateStringInContext(ctx context.Context, contextID, expression string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("Camoufox session 已关闭")
	}
	return s.client.evaluateString(ctx, contextID, expression)
}

// EvaluateNode 在顶层 context 返回 DOM 节点的 BiDi sharedId。
func (s *Session) EvaluateNode(ctx context.Context, expression string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("Camoufox session 已关闭")
	}
	return s.client.evaluateNode(ctx, s.contextID, expression)
}

// FindFrame 返回 URL 包含 urlContains 的第一个子帧 context ID；找不到返回空串。
func (s *Session) FindFrame(ctx context.Context, urlContains string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("Camoufox session 已关闭")
	}
	if urlContains == "" {
		return s.contextID, nil
	}
	tree, err := s.client.command(ctx, "browsingContext.getTree", map[string]any{"maxDepth": 10, "root": s.contextID})
	if err != nil {
		return "", err
	}
	var walk func(node map[string]any) string
	walk = func(node map[string]any) string {
		if url, _ := node["url"].(string); strings.Contains(url, urlContains) {
			if cid, _ := node["context"].(string); cid != "" {
				return cid
			}
		}
		children, _ := node["children"].([]any)
		for _, child := range children {
			if c, ok := child.(map[string]any); ok {
				if found := walk(c); found != "" {
					return found
				}
			}
		}
		return ""
	}
	// browsingContext.getTree 的 result 键：Camoufox（Firefox 系 BiDi）用 "contexts"，
	// 标准 BiDi 用 "tree"。两个都尝试，取数组再遍历节点。
	nodes, ok := tree["contexts"].([]any)
	if !ok {
		nodes, ok = tree["tree"].([]any)
	}
	if ok {
		for _, n := range nodes {
			if node, ok := n.(map[string]any); ok {
				if found := walk(node); found != "" {
					return found, nil
				}
			}
		}
	}
	return "", nil
}

// AllContexts 返回当前页面所有 browsing context（含跨域 iframe）的 context ID，
// 用于点击逻辑遍历子帧——FindFrame 仅靠 URL 子串可能漏掉跨域 applet 帧（run.app）。
func (s *Session) AllContexts(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Camoufox session 已关闭")
	}
	tree, err := s.client.command(ctx, "browsingContext.getTree", map[string]any{"maxDepth": 10, "root": s.contextID})
	if err != nil {
		return nil, err
	}
	var out []string
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if cid, _ := node["context"].(string); cid != "" {
			out = append(out, cid)
		}
		children, _ := node["children"].([]any)
		for _, child := range children {
			if c, ok := child.(map[string]any); ok {
				walk(c)
			}
		}
	}
	// 取 getTree result 的数组再遍历节点；键名 Camoufox 用 "contexts"，标准 BiDi 用 "tree"。
	nodes, ok := tree["contexts"].([]any)
	if !ok {
		nodes, ok = tree["tree"].([]any)
	}
	if ok {
		for _, n := range nodes {
			if node, ok := n.(map[string]any); ok {
				walk(node)
			}
		}
	}
	return out, nil
}

// WaitFor 在顶层 context 轮询表达式直至为 true 或超时。
func (s *Session) WaitFor(ctx context.Context, expression string, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("Camoufox session 已关闭")
	}
	return s.client.waitFor(ctx, s.contextID, expression, timeout)
}

// BrowserPID 返回 Camoufox 浏览器主进程 PID（OS 级输入注入定位窗口用）。
// 会话未启动或已关闭时返回 0。
func (s *Session) BrowserPID() int {
	if s == nil || s.process == nil || s.process.command == nil || s.process.command.Process == nil {
		return 0
	}
	return s.process.command.Process.Pid
}

// Close 结束 BiDi session 并清理 Camoufox profile。
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	client := s.client
	connection := s.connection
	process := s.process
	s.mu.Unlock()
	if client != nil && connection != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = client.command(closeCtx, "session.end", map[string]any{})
		cancel()
		_ = connection.Close()
	}
	return process.Close()
}
