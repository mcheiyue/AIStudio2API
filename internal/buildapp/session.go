package buildapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

// AppletURL 是默认 Build App applet（iBUHub 写死、已被 Google 403 的公共实例 cab9ab6c）。
// 实战应换成fork自 cab9ab6c 的、2267 自己的 app（带 ProxyClient、用 2267 会话鉴权）。
const AppletURL = "https://ai.studio/apps/cab9ab6c-44f9-4e7a-8972-037f8ae177ab"

// Session 持有一个 Build App applet 浏览器会话 + 对应 WS 连接的 authIndex。
type Session struct {
	cam       *camoufoxnative.Session
	authIndex int
	ws        *Server
	click     func(labels []string) string
}

// NewSession 创建一个手动构建的 Session（跳过 LaunchApplet 的自动点击流程）。
func NewSession(cam *camoufoxnative.Session, ws *Server, authIndex int) *Session {
	return &Session{cam: cam, authIndex: authIndex, ws: ws}
}

// AuthIndexResponderJS 必须在 applet 加载前注册：applet 的 ProxyClient 会向父窗口 postMessage 请求
// requestAuthIndex，父窗口回应 authIndexResponse 后 applet 才会连 ws://127.0.0.1:9998。
// 形态对齐 bidi.go installLocalStorage（arrow 函数表达式，由 BiDi addPreloadScript 调用），
// 不要写成 IIFE——IIFE 自执行后返回 undefined，等于没注册。仅顶层窗口生效（iframe 向 parent post）。
const AuthIndexResponderJS = `() => {
  window.__responderActive = true;
  if (window === window.top) {
    window.addEventListener('message', function (event) {
      if (event.data && event.data.type === 'requestAuthIndex') {
        try { event.source.postMessage({ type: 'authIndexResponse', authIndex: 0 }, '*'); } catch (e) {}
      }
    });
  }
}`

// LaunchApplet 启动 Camoufox、加载 2267 会话、导航到 applet、点穿引导并激活 Preview（ProxyClient 连 9998），
// 直到 ws 报告该 authIndex 就绪。appletURL 为空时回退到默认 AppletURL。
func LaunchApplet(ctx context.Context, opts camoufoxnative.Options, ws *Server, authIndex int, appletURL string) (*Session, error) {
	return launchApplet(ctx, opts, ws, authIndex, appletURL)
}

func launchApplet(ctx context.Context, opts camoufoxnative.Options, ws *Server, authIndex int, appletURL string) (*Session, error) {
	cam, err := camoufoxnative.StartSession(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("start camoufox session: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = cam.Close()
		}
	}()

	// 导航前注入 authIndex responder（applet 连 9998 的门控）+ 点击捕获器（诊断用）
	if err := cam.AddInitScript(ctx, AuthIndexResponderJS); err != nil {
		return nil, fmt.Errorf("add authIndex init script: %w", err)
	}
	// 点击捕获器：记录最近 20 次点击的完整元素链，诊断 Launch! 按钮结构
	if err := cam.AddInitScript(ctx, `(function(){
		window.__clickLog = [];
		document.addEventListener('click', function(ev) {
			var el = ev.target;
			var chain = [];
			for (var n = el; n && chain.length < 8; n = n.parentElement) {
				var info = n.tagName.toLowerCase();
				if (n.id) info += '#' + n.id;
				if (n.className && typeof n.className === 'string') info += '.' + n.className.split(' ').slice(0,3).join('.');
				var role = n.getAttribute && n.getAttribute('role');
				if (role) info += '[role=' + role + ']';
				info += ': ' + (n.textContent || '').trim().slice(0,50);
				chain.push(info);
			}
			window.__clickLog.push({ts: Date.now(), tag: el.tagName, id: el.id, cls: (typeof el.className === 'string' ? el.className : '').slice(0,100), role: el.getAttribute && el.getAttribute('role'), text: (el.textContent || '').trim().slice(0,80), chain: chain});
			if (window.__clickLog.length > 20) window.__clickLog.shift();
		}, true);
	})();`); err != nil {
		log.Printf("[buildapp] click capture inject err: %v", err)
	}
	log.Printf("[buildapp] authIndex responder + click capture injected")

	if appletURL == "" {
		appletURL = AppletURL
	}
	// Google applet 页偶发 NS_ERROR_NET_RESET（Camoufox 网络抖动 / Clash HY2 瞬时重置），
	// 多次重试导航避免一次失败即放弃（最多 8 次，退避 3s）
	var navErr error
	for attempt := 1; attempt <= 8; attempt++ {
		if navErr = cam.Navigate(ctx, appletURL); navErr == nil {
			break
		}
		if strings.Contains(navErr.Error(), "NS_ERROR_NET_RESET") || strings.Contains(navErr.Error(), "net::") {
			log.Printf("[buildapp] navigate attempt %d 失败（%v），重试", attempt, navErr)
			time.Sleep(3 * time.Second)
			continue
		}
		return nil, fmt.Errorf("navigate applet: %w", navErr)
	}
	if navErr != nil {
		return nil, fmt.Errorf("navigate applet（重试 3 次均失败）: %w", navErr)
	}
	log.Printf("[buildapp] navigated to applet %s", appletURL)

	// 等页面稳定
	time.Sleep(3 * time.Second)

	// 点穿引导 + 激活 Preview（ProxyClient 才会连 9998）
	onboard := []string{"Skip", "Got it", "Continue to the app", "Allow", "Accept", "同意", "继续", "close", "开"}
	runLabels := []string{"Preview", "Run", "▶", "运行", "预览", "Update preview", "更新预览"}
	// 引导/Preview 在跨域 iframe（run.app）内，需对子帧上下文点击
	frameURLs := []string{"run.app", "bscframe", "_/bscframe", "aistudio.google.com/app/_"}
	clickIn := func(contextID string, labels []string) (string, error) {
		js := fmt.Sprintf(`(function(){
			const labels = %s;
			const norm = s => (s||'').trim().toLowerCase();
			const match = el => {
				const t = norm(el.innerText || el.textContent || '');
				if (!t) return false;
				if (el.offsetParent === null) return false;
				return labels.some(l => {
					const nl = norm(l);
					if (t === nl) return true;
					if (nl.length >= 6 && t.includes(nl) && t.length <= nl.length * 3) return true;
					return false;
				});
			};
			const fire = el => {
				try { for (const type of ['pointerdown','mousedown','pointerup','mouseup','click']) el.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true,view:window})); } catch(e){ el.click(); }
				return (el.innerText || el.textContent || '').trim();
			};
			// 第一遍：普通 DOM 搜索（保持原有可见性检查）
			const sels = ['button','[role=button]','a','p','span','div'];
			for (const sel of sels) {
				for (const el of document.querySelectorAll(sel)) {
					if (match(el)) return fire(el);
				}
			}
			// 第二遍：穿透 Shadow DOM（ms-applet-viewer 等 Web Component 内的按钮）
			// Shadow DOM 内元素 offsetParent 通常为 null，跳过可见性检查，只做文本匹配
			function searchShadow(root) {
				for (const el of root.querySelectorAll('*')) {
					if (el.shadowRoot) {
						for (const se of el.shadowRoot.querySelectorAll('*')) {
							const t = norm(se.innerText || se.textContent || '');
							if (!t) continue;
							for (const l of labels) {
								const nl = norm(l);
								if (t === nl) return fire(se);
								if (nl.length >= 6 && t.includes(nl) && t.length <= nl.length * 3) return fire(se);
							}
						}
						const found = searchShadow(el.shadowRoot);
						if (found) return found;
					}
				}
				return '';
			}
			return searchShadow(document);
		})()`, MustJSON(labels))
		var res string
		var err error
		if contextID == "" {
			res, err = cam.EvaluateString(ctx, js)
		} else {
			res, err = cam.EvaluateStringInContext(ctx, contextID, js)
		}
		if err != nil {
			if contextID != "" {
				log.Printf("[buildapp] clickIn err (ctx %s): %v", contextID, err)
			}
			return "", err
		}
		res = strings.TrimSpace(res)
		if res == "''" || res == "\"\"" {
			return "", nil
		}
		return res, nil
	}
	clickFn := func(labels []string) string {
		// 已知子帧优先（run.app 等）
		for _, fu := range frameURLs {
			if fc, e := cam.FindFrame(ctx, fu); e == nil && fc != "" {
				if r, _ := clickIn(fc, labels); r != "" {
					return r
				}
			}
		}
		// 兜底：遍历所有 browsing context（含跨域 iframe），避免漏掉引导/Preview 按钮
		if all, e := cam.AllContexts(ctx); e == nil {
			for _, fc := range all {
				if r, _ := clickIn(fc, labels); r != "" {
					return r
				}
			}
		}
		if r, _ := clickIn("", labels); r != "" {
			return r
		}
		return ""
	}

	activateDeadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(activateDeadline) {
		if ws.Ready(authIndex) {
			log.Printf("[buildapp] applet WS ready authIndex=%d", authIndex)
			failed = false
			return &Session{cam: cam, authIndex: authIndex, ws: ws, click: clickFn}, nil
		}
		// 先点 Preview/Run（可能需多次才命中真实按钮）。
		// 注意：已在 Preview 视图时不能再点，否则会 toggle 回 Code 视图，
		// run.app iframe 随之不渲染。以 URL 是否含 showPreview=true 为判据。
		inPreview := false
		if cur, e := cam.EvaluateString(ctx, `window.location.href`); e == nil {
			inPreview = strings.Contains(cur, "showPreview=true")
		}
		if !inPreview {
			if c := clickFn(runLabels); c != "" {
				log.Printf("[buildapp] clicked run-mode label: %s (waiting WS)", c)
			}
		}
		// 顺手点引导，避免引导对话框挡住 Preview
		for _, lbl := range onboard {
			if c := clickFn([]string{lbl}); c != "" {
				log.Printf("[buildapp] onboard click: %s", c)
				break
			}
		}
		time.Sleep(2500 * time.Millisecond)
	}
	return nil, fmt.Errorf("applet WS not ready within 150s for authIndex=%d", authIndex)
}

// Submit 构造 proxy_request 经 WS 转发给 applet，返回响应消息通道。
// DumpAppletDiagnostics 转储所有 browsing context 的 URL 与页面文本。
// applet 的 Logger.output 写在面板 DOM 里，dump body innerText 即可看到
// ProxyClient 收到 proxy_request 后的自述（Received request / Message processing error 等）。
func (s *Session) DumpAppletDiagnostics(ctx context.Context) {
	contexts, err := s.cam.AllContexts(ctx)
	if err != nil {
		log.Printf("[buildapp] dump: AllContexts 失败: %v", err)
		return
	}
	for _, fc := range contexts {
		url, _ := s.cam.EvaluateStringInContext(ctx, fc, "location.href")
		info, _ := s.cam.EvaluateStringInContext(ctx, fc, `(function(){
			var sw = null;
			try { sw = (navigator.serviceWorker && navigator.serviceWorker.controller) ? navigator.serviceWorker.controller.scriptURL : null; } catch(e) { sw = 'err:' + e.message; }
			var fetchStr = '';
			try { fetchStr = String(window.fetch).slice(0, 150); } catch(e) { fetchStr = 'err:' + e.message; }
			var gen = [];
			try {
				gen = performance.getEntriesByType('resource')
					.filter(function(e){ return e.name.indexOf('generativelanguage') !== -1; })
					.map(function(e){ return {n: e.name.slice(-70), t: e.initiatorType, d: Math.round(e.duration), s: (e.responseStatus === undefined ? -1 : e.responseStatus)}; });
			} catch(e) {}
			var launchBtns = [];
			try {
				function deepQuery(root, sel) {
					var res = Array.from(root.querySelectorAll(sel));
					root.querySelectorAll('*').forEach(function(el) {
						if (el.shadowRoot) res = res.concat(deepQuery(el.shadowRoot, sel));
					});
					return res;
				}
				launchBtns = deepQuery(document, 'button, [role=button]')
					.filter(function(b){ return b.innerText.trim().toLowerCase().includes('launch'); })
					.map(function(b){ return {tag: b.tagName, text: b.innerText.trim().slice(0,60), hasShadow: !!b.getRootNode().host, visible: b.offsetParent !== null}; });
			} catch(e) {}
			var txt = document.body ? document.body.innerText.replace(/\s+/g, ' ').slice(-400) : '<no body>';
			var clickLog = window.__clickLog || [];
			return JSON.stringify({sw: sw, fetch: fetchStr, gen: gen, launchBtns: launchBtns, clickLog: clickLog.slice(-5), tail: txt});
		})()`)
		log.Printf("[buildapp] dump ctx=%s url=%s", fc, strings.TrimSpace(url))
		log.Printf("[buildapp] dump info: %s", strings.TrimSpace(info))
	}
}

func (s *Session) Submit(req ProxyRequest) (<-chan AppletMessage, error) {
	ch, err := s.ws.Submit(s.authIndex, req)
	if err != nil {
		return nil, err
	}
	// run.app 帧的 window.fetch 被 Google 运行时替换为 `await bootstrapChannel` 版本：
	// 不点 Launch! 则 fetch 永久挂起（零网络请求、applet 零回包）。
	// Launch! 点击触发 bootstrapChannel 放行；请求期循环点击直到窗口结束。
	if s.click != nil {
		go s.clickLaunchDuringRequest()
	}
	return ch, nil
}

// clickLaunchDuringRequest 在请求飞行期间持续点击 Launch!（顶层帧，rocket_launch 图标按钮）。
// 页面可能落在 Code 视图（Launch! 只在 Preview 工具栏），先点 Preview 切视图再点 Launch!。
// bootstrapChannel 放行后继续点击无害，固定窗口后停止。
func (s *Session) clickLaunchDuringRequest() {
	deadline := time.After(180 * time.Second)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			return
		case <-tick.C:
		}
		if c := s.click([]string{"Launch!"}); c != "" {
			log.Printf("[buildapp] clicked request-time Launch: %s", c)
			continue
		}
		if c := s.click([]string{"Preview"}); c != "" {
			log.Printf("[buildapp] clicked request-time Preview (finding Launch): %s", c)
		}
	}
}

// SubmitNoAutoClick 发送 proxy_request 但不启动请求期自动点击。用于诊断模式：手动观察 Launch! 按钮。
func (s *Session) SubmitNoAutoClick(req ProxyRequest) (<-chan AppletMessage, error) {
	return s.ws.Submit(s.authIndex, req)
}

// InjectScript 向当前所有帧注入 JS 脚本。
func (s *Session) InjectScript(ctx context.Context, js string) error {
	return s.cam.AddInitScript(ctx, js)
}

// Done 清理请求队列。
func (s *Session) Done(requestID string) { s.ws.Done(requestID) }

// ClickLabel 在已知子帧/全上下文/顶层依次尝试点击指定文本标签（仅供取证/诊断）。
func (s *Session) ClickLabel(ctx context.Context, labels []string) string { return s.click(labels) }

// CollectFetchEvidence 在全部 browsing context 注入 fetch 调用记录与 postMessage 记录，
// 返回 summary（每帧一条）。用于取证 Launch!/Preview 放行后 fetch 的实际参数与凭证来源。
func (s *Session) CollectFetchEvidence(ctx context.Context) (string, error) {
	inject := `(function(){
		if(!window.__fetchLog){ window.__fetchLog=[]; }
		if(!window.__fetchWrapped && typeof window.fetch === 'function'){
			var orig = window.fetch;
			window.fetch = function(input, init){
				var rec = { url: String((typeof input === 'string') ? input : (input && input.url || '')), method: (init && init.method) || ((input && input.method) || 'GET'), cred: (init && init.credentials) || '', hdrs: [] };
				if (init && init.headers) { try { rec.hdrs = Object.keys(init.headers); rec.hdrTxt = JSON.stringify(init.headers).slice(0, 220); } catch(e) { rec.hdrTxt = 'err'; } }
				rec.start = Date.now(); rec.status = 'pending';
				var p = orig.apply(this, arguments);
				p.then(function(r){ rec.status = String(r.status); rec.ok = r.ok; }).catch(function(e){ rec.status = 'ERR:' + String(e && e.message).slice(0, 80); });
				window.__fetchLog.push(rec);
				if (window.__fetchLog.length > 40) window.__fetchLog.shift();
				return p;
			};
			window.__fetchWrapped = true;
		}
		if(!window.__msgLog){
			window.__msgLog = [];
			addEventListener('message', function(e){
				var sum = '';
				try { sum = (typeof e.data === 'string') ? e.data.slice(0, 160) : JSON.stringify(e.data).slice(0, 160); } catch(err) { sum = String(e.data).slice(0, 160); }
				window.__msgLog.push({ origin: e.origin || '', type: typeof e.data, sum: sum });
				if (window.__msgLog.length > 40) window.__msgLog.shift();
			});
		}
		if(!window.__xhrLog){ window.__xhrLog=[]; }
		if(!window.__xhrWrapped && window.XMLHttpRequest){
			var proto = XMLHttpRequest.prototype;
			var origOpen = proto.open; var origSend = proto.send; var origSet = proto.setRequestHeader;
			proto.open = function(m, u){ this.__rec = { m: m, u: String(u), start: Date.now(), h: {} }; return origOpen.apply(this, arguments); };
			proto.setRequestHeader = function(n, v){ if (this.__rec) { try { this.__rec.h[n] = String(v).slice(0, 160); } catch(e){} } return origSet.apply(this, arguments); };
			proto.send = function(body){ var self = this; self.addEventListener('load', function(){ if (self.__rec) { self.__rec.status = self.status; self.__rec.body = (typeof body === 'string' ? body.slice(0, 120) : ''); window.__xhrLog.push(self.__rec); if (window.__xhrLog.length > 40) window.__xhrLog.shift(); } }); return origSend.apply(this, arguments); };
			window.__xhrWrapped = true;
		}
		return 'ok';
	})()`
	var builder strings.Builder
	contexts, err := s.cam.AllContexts(ctx)
	if err != nil {
		return "", err
	}
	for _, fc := range contexts {
		if _, err := s.cam.EvaluateStringInContext(ctx, fc, inject); err != nil {
			builder.WriteString(fmt.Sprintf("[ctx %s] inject err: %v\n", fc, err))
			continue
		}
		readback := `JSON.stringify({ fetch: (window.__fetchLog || []), xhr: (window.__xhrLog || []), msg: (window.__msgLog || []), href: location.href.slice(0, 120) })`
		raw, err := s.cam.EvaluateStringInContext(ctx, fc, readback)
		if err != nil {
			builder.WriteString(fmt.Sprintf("[ctx %s] read err: %v\n", fc, err))
			continue
		}
		builder.WriteString(raw)
		builder.WriteString("\n")
	}
	return builder.String(), nil
}

// EvalAllContexts 在每个 browsing context 求值同一表达式，逐帧返回结果（诊断用）。
func (s *Session) EvalAllContexts(ctx context.Context, js string) (string, error) {
	contexts, err := s.cam.AllContexts(ctx)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, fc := range contexts {
		raw, err := s.cam.EvaluateStringInContext(ctx, fc, js)
		if err != nil {
			builder.WriteString(fmt.Sprintf("[ctx %s] err: %v\n", fc, err))
			continue
		}
		builder.WriteString(fmt.Sprintf("[ctx %s] %s\n", fc, strings.TrimSpace(raw)))
	}
	return builder.String(), nil
}

// RestoreClientWindow 恢复 BiDi client window 并打印原始窗口信息（诊断用）。
func (s *Session) RestoreClientWindow(ctx context.Context) error {
	return s.cam.RestoreClientWindow(ctx)
}

// ClientWindowRect 返回 Camoufox 可见 client window 的屏幕坐标与尺寸（OS 输入注入用）。
func (s *Session) ClientWindowRect(ctx context.Context) (x, y, width, height float64, err error) {
	return s.cam.ClientWindowRect(ctx)
}

// ClickAt 在 Camoufox 窗口视口坐标 (viewportX, viewportY) 处执行 OS 级真实左键点击。
// Windows 走 user32（GetClientRect/ClientToScreen/SetCursorPos/mouse_event）；
// Linux 走 xdotool（XTest，Xvfb 虚拟屏）。
func (s *Session) ClickAt(viewportX, viewportY int) error {
	pid := s.cam.BrowserPID()
	if pid == 0 {
		return fmt.Errorf("Camoufox 浏览器进程不可用")
	}
	return osClick(pid, viewportX, viewportY)
}

// ClickLaunch 定位顶层 Launch! 覆盖层并用 OS 级真实鼠标点击其中心。
// 返回是否定位到（未定位到不视为错误，由调用方重试）。
func (s *Session) ClickLaunch() (bool, error) {
	hit, err := s.LocateLaunch(context.Background())
	if err != nil {
		return false, nil // 未定位到：调用方重试
	}
	if err := s.ClickAt(int(hit.CX), int(hit.CY)); err != nil {
		return true, err
	}
	return true, nil
}

// Close 关闭浏览器会话。
func (s *Session) Close() error { return s.cam.Close() }

func MustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
