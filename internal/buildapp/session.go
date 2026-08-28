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
}

// authIndexResponderJS 必须在 applet 加载前注册：applet 的 ProxyClient 会向父窗口 postMessage 请求
// requestAuthIndex，父窗口回应 authIndexResponse 后 applet 才会连 ws://127.0.0.1:9998。
// 形态对齐 bidi.go installLocalStorage（arrow 函数表达式，由 BiDi addPreloadScript 调用），
// 不要写成 IIFE——IIFE 自执行后返回 undefined，等于没注册。仅顶层窗口生效（iframe 向 parent post）。
const authIndexResponderJS = `() => {
  window.__responderActive = true;
  window.__messagesSeen = [];
  if (window === window.top) {
    window.addEventListener('message', function (event) {
      try { window.__messagesSeen.push(String(event.data).slice(0, 300)); } catch (e) {}
      if (event.data && event.data.type === 'requestAuthIndex') {
        window.__authIndexRequested = true;
        try { event.source.postMessage({ type: 'authIndexResponse', authIndex: 0 }, '*'); } catch (e) {}
      }
    });
  }
}`

// LaunchApplet 启动 Camoufox、加载 2267 会话、导航到 applet、点穿引导并激活 Preview（ProxyClient 连 9998），
// 直到 ws 报告该 authIndex 就绪。appletURL 为空时回退到默认 AppletURL。
func LaunchApplet(ctx context.Context, opts camoufoxnative.Options, ws *Server, authIndex int, appletURL string) (*Session, error) {
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

	// 导航前注册 authIndex responder（applet 连 9998 的门控）
	if err := cam.AddInitScript(ctx, authIndexResponderJS); err != nil {
		return nil, fmt.Errorf("add authIndex init script: %w", err)
	}
	log.Printf("[buildapp] authIndex responder injected")

	if appletURL == "" {
		appletURL = AppletURL
	}
	if err := cam.Navigate(ctx, appletURL); err != nil {
		return nil, fmt.Errorf("navigate applet: %w", err)
	}
	log.Printf("[buildapp] navigated to applet %s", appletURL)

	// 诊断：确认 authIndex responder 预加载脚本真在顶层窗口跑了
	if active, err := cam.EvaluateString(ctx, `String(window.__responderActive)`); err == nil {
		log.Printf("[buildapp] responder active in top window: %s", strings.TrimSpace(active))
	} else {
		log.Printf("[buildapp] responder check error: %v", err)
	}
	// 诊断：applet 是否向父窗口发了 requestAuthIndex（握手是否触发）
	if req, err := cam.EvaluateString(ctx, `String(window.__authIndexRequested)`); err == nil {
		log.Printf("[buildapp] authIndex requested by applet: %s", strings.TrimSpace(req))
	}
	// 诊断：顶层窗口收到的所有 postMessage 类型
	if msgs, err := cam.EvaluateString(ctx, `JSON.stringify(window.__messagesSeen||[])`); err == nil {
		log.Printf("[buildapp] postMessage types seen: %s", strings.TrimSpace(msgs))
	}

	// 诊断：dump 所有（含同域 iframe）可见按钮文本，确认真实 UI
	if dump, err := cam.EvaluateString(ctx, `(function(){
		const docs=[document];
		for(const f of document.querySelectorAll('iframe')){try{if(f.contentDocument)docs.push(f.contentDocument);}catch(e){}}
		const els=[];
		for(const d of docs){for(const e of d.querySelectorAll('button,[role=button]')){if(e.offsetParent!==null){const t=(e.innerText||'').trim();if(t)els.push(t);}}}
		return JSON.stringify(els);
	})()`); err == nil {
		log.Printf("[buildapp] visible buttons: %s", dump)
	}
	// 等页面稳定
	time.Sleep(3 * time.Second)

	// 点穿引导 + 激活 Preview（ProxyClient 才会连 9998）
	onboard := []string{"Continue to the app", "Skip", "Next", "Got it", "Allow", "Accept", "同意", "继续"}
	runLabels := []string{"Preview", "Run", "▶", "运行", "预览", "Update preview", "更新预览"}
	// 引导/Preview 在跨域 iframe（run.app）内，需对子帧上下文点击
	frameURLs := []string{"run.app", "bscframe", "_/bscframe", "aistudio.google.com/app/_"}
	clickIn := func(contextID string, labels []string) (string, error) {
		js := fmt.Sprintf(`(function(){
			const labels = %s;
			const norm = s => (s||'').trim().toLowerCase();
			const hit = el => {
				const t = norm(el.innerText);
				if (!t || el.offsetParent === null) return false;
				return labels.some(l => t === norm(l) || t.includes(norm(l)));
			};
			// 优先真实交互元素
			for (const sel of ['button','[role=button]','a']) {
				for (const el of document.querySelectorAll(sel)) {
					if (hit(el)) {
						try { for (const type of ['pointerdown','mousedown','pointerup','mouseup','click']) el.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true,view:window})); } catch(e){ el.click(); }
						return el.innerText.trim();
					}
				}
			}
			// 退而求其次：div 精确匹配
			for (const el of document.querySelectorAll('div')) {
				if (norm(el.innerText) === norm(labels[0]) || (labels.some(l=>norm(el.innerText)===norm(l)))) {
					try { for (const type of ['pointerdown','mousedown','pointerup','mouseup','click']) el.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true,view:window})); } catch(e){ el.click(); }
					return el.innerText.trim();
				}
			}
			return '';
		})()`, mustJSON(labels))
		var res string
		var err error
		if contextID == "" {
			res, err = cam.EvaluateString(ctx, js)
		} else {
			res, err = cam.EvaluateStringInContext(ctx, contextID, js)
		}
		if err != nil {
			return "", err
		}
		res = strings.TrimSpace(res)
		if res == "''" || res == "\"\"" {
			return "", nil
		}
		return res, nil
	}
	clickFn := func(labels []string) string {
		for _, fu := range frameURLs {
			if fc, e := cam.FindFrame(ctx, fu); e == nil && fc != "" {
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

	// 反复点引导 + 探测 Preview，直到激活或超时（UI 点击偶发不稳定，拉长窗口）
	activateDeadline := time.Now().Add(120 * time.Second)
	activated := false
	for time.Now().Before(activateDeadline) {
		// 先尝试点 Preview/Run
		if c := clickFn(runLabels); c != "" {
			log.Printf("[buildapp] activated run mode: %s", c)
			activated = true
			break
		}
		// 否则点引导
		for _, lbl := range onboard {
			if c := clickFn([]string{lbl}); c != "" {
				log.Printf("[buildapp] onboard click: %s", c)
				time.Sleep(1200 * time.Millisecond)
				break
			}
		}
		time.Sleep(800 * time.Millisecond)
	}
	if !activated {
		return nil, fmt.Errorf("failed to activate run mode within 120s")
	}

	// 等 WS 连接就绪（applet ProxyClient 连 9998）；Preview 后 applet 可能需数秒才连
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if ws.Ready(authIndex) {
			log.Printf("[buildapp] applet WS ready authIndex=%d", authIndex)
			// ProxyClient 已激活，此刻再读 postMessage 全量（requestAuthIndex 在 run mode 激活后才发）
			if msgs, err := cam.EvaluateString(ctx, `JSON.stringify(window.__messagesSeen||[])`); err == nil {
				log.Printf("[buildapp] postMessage seen after WS ready: %s", strings.TrimSpace(msgs))
			}
			if req, err := cam.EvaluateString(ctx, `String(window.__authIndexRequested)`); err == nil {
				log.Printf("[buildapp] authIndex requested by applet: %s", strings.TrimSpace(req))
			}
			failed = false
			return &Session{cam: cam, authIndex: authIndex, ws: ws}, nil
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("applet WS not ready within 90s for authIndex=%d", authIndex)
}

// Submit 构造 proxy_request 经 WS 转发给 applet，返回响应消息通道。
func (s *Session) Submit(req ProxyRequest) (<-chan AppletMessage, error) {
	return s.ws.Submit(s.authIndex, req)
}

// Done 清理请求队列。
func (s *Session) Done(requestID string) { s.ws.Done(requestID) }

// Close 关闭浏览器会话。
func (s *Session) Close() error { return s.cam.Close() }

// TryGetGapiToken 在 applet run 上下文尝试取出会话的 gapi OAuth Bearer。
// iBUHub build.js(#232) 靠 gapi.auth.getToken() 取 token 注入 fetch；本方法用 BiDi 在页面作用域取同样的值，
// 用于免新建 API key 直接给 proxy_request 头带凭证。取不到（module scope 不可达）则返回空串。
func (s *Session) TryGetGapiToken(ctx context.Context) string {
	probes := []string{
		`(async () => { try { const g = window.gapi; if (!g || !g.auth) return 'no_window_gapi'; const t = await g.auth.getToken(); if (!t) return 'token_null'; if (typeof t === 'string') return t; if (t.access_token) return t.access_token; if (t.getAccessToken) return t.getAccessToken(); return JSON.stringify(t); } catch(e){ return 'err:'+e.message; } })()`,
		`(async () => { try { if (typeof gapi === 'undefined' || !gapi.auth) return 'no_gapi_global'; const t = await gapi.auth.getToken(); if (!t) return 'token_null'; if (typeof t === 'string') return t; if (t.access_token) return t.access_token; if (t.getAccessToken) return t.getAccessToken(); return JSON.stringify(t); } catch(e){ return 'err:'+e.message; } })()`,
		// gapi.client 是否已初始化并持有 token
		`(async () => { try { if (!window.gapi || !gapi.client) return 'no_gapi_client'; const r = await gapi.client.request({path:'https://generativelanguage.googleapis.com/v1beta/models?key=test', method:'GET'}); return 'client_req_ok'; } catch(e){ return 'client_err:'+(e&&e.message?e.message:'?'); } })()`,
		// 扫描 document.cookie 里像 OAuth/access token 的值（非 httpOnly 可见部分）
		`(() => { try { const c = document.cookie||''; const toks = c.split(';').map(s=>s.trim()).filter(s=>/token|oauth|access|sa-|sid/i.test(s.split('=')[0])); return 'cookies:'+toks.slice(0,12).join('|'); } catch(e){ return 'err'; } })()`,
		// 枚举 window 上像 JWT/长 token 的全局键
		`(() => { try { return Object.getOwnPropertyNames(window).filter(k => { try { const v=window[k]; return typeof v==='string' && v.length>40 && /[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\./.test(v); } catch(e){ return false; } }).slice(0,10).join(','); } catch(e){ return 'err'; } })()`,
	}
	diags := make([]string, 0, len(probes))
	for _, p := range probes {
		v, err := s.cam.EvaluateString(ctx, p)
		if err != nil {
			log.Printf("[buildapp] gapi probe err: %v", err)
			diags = append(diags, "err:"+err.Error())
			continue
		}
		v = strings.TrimSpace(v)
		log.Printf("[buildapp] gapi probe (%s...) => %s", p[:min(len(p), 46)], v)
		if strings.HasPrefix(v, "ya29") {
			return v
		}
		if strings.Contains(v, "access_token") {
			var m struct {
				AccessToken string `json:"access_token"`
			}
			if json.Unmarshal([]byte(v), &m) == nil && m.AccessToken != "" {
				return m.AccessToken
			}
		}
		diags = append(diags, v)
	}
	log.Printf("[buildapp] gapi token 未直接取到，诊断: %s", strings.Join(diags, " || "))
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
