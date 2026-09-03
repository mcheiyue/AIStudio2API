package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/buildapp"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

func main() {
	storageState := flag.String("storage-state", "", "2267 storage-state.json 路径")
	camoufoxPath := flag.String("camoufox", "", "camoufox.exe 路径")
	authIndexFlag := flag.Int("authindex", 0, "WS authIndex")
	addr := flag.String("addr", ":9998", "WS 监听地址")
	model := flag.String("model", "gemini-2.5-flash", "测试模型")
	applet := flag.String("applet", "", "覆盖默认 applet URL（如 fork 自 cab9ab6c 的 2267 自有 app）")
	requestTimeout := flag.Duration("request-timeout", 320*time.Second, "等待 applet 请求完成的最长时间")
	extension := flag.String("extension", "", "可选：CORS bypass 扩展目录路径")
	noClick := flag.Bool("no-click", false, "诊断模式：跳过所有自动点击，等待用户手动操作，周期性 dump clickLog")
	trustedClick := flag.Bool("trusted-click", false, "实验模式：用 BiDi 真实鼠标点击顶层 Launch!，不使用合成点击")
	osInput := flag.Bool("os-input", false, "OS input mode: skip BiDi click, wait for external OS-level keyboard/mouse after Launch! appears")
	osClick := flag.Bool("os-click", false, "OS click mode: 内置 OS 级真实鼠标点击 Launch!（等价人工点击，模型测试用）")
	toolTest := flag.Bool("tool-test", false, "工具调用测试：body 带 functionDeclarations(get_weather)，验证 applet 透传 tools 后 Google 是否回包")
	fetchEvidence := flag.Bool("fetch-evidence", false, "取证模式：注入 fetch/postMessage 记录，点 Preview 放行后 dump 实际请求参数（无需人工）")
	headless := flag.Bool("headless", false, "无头模式（内存对照采样用）")
	flag.Parse()

	if *storageState == "" || *camoufoxPath == "" {
		log.Fatal("需要 -storage-state 和 -camoufox")
	}

	ws := buildapp.NewServer()
	ws.SetHooks(
		func(i int) { log.Printf("[test] applet 已连接 authIndex=%d", i) },
		func(i int) { log.Printf("[test] applet 断开 authIndex=%d", i) },
	)
	go func() {
		if err := ws.Start(*addr); err != nil {
			log.Printf("[test] WS 服务错误: %v", err)
		}
	}()
	time.Sleep(500 * time.Millisecond)

	opts := camoufoxnative.Options{
		ExecutablePath:   *camoufoxPath,
		StorageStatePath: *storageState,
		Locale:           "en-US",
		Timezone:         "America/New_York",
		Proxy:            os.Getenv("BUILDAPP_PROXY"),
		ProxyBypass:      "127.0.0.1,localhost",
		Headless:         *headless,
		Log:              os.Stderr,
		StartupProgress: func(s camoufoxnative.StartupStage) {
			log.Printf("[test] startup stage: %s", s)
		},
	}
	if *extension != "" {
		opts.Extensions = []string{*extension}
		log.Printf("[test] 加载扩展: %s", *extension)
	}

	if *trustedClick && *noClick {
		log.Fatal("-trusted-click 与 -no-click 不能同时使用")
	}
	if *fetchEvidence && (*trustedClick || *noClick) {
		log.Fatal("-fetch-evidence 不能与 -trusted-click/-no-click 同时使用")
	}
	if *fetchEvidence {
		runFetchEvidenceProbe(opts, ws, *authIndexFlag, *applet, *model, *requestTimeout)
		return
	}
	if *trustedClick {
		runTrustedClickProbe(opts, ws, *authIndexFlag, *applet, *model, *requestTimeout, *osInput, *osClick, *toolTest)
		return
	}

	// -no-click 模式：启动 Camoufox + 注入脚本 + 导航，但零自动点击，周期性 dump clickLog
	if *noClick {
		runManualMode(opts, ws, *authIndexFlag, *applet, *model)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second+*requestTimeout+30*time.Second)
	defer cancel()

	sess, err := buildapp.LaunchApplet(ctx, opts, ws, *authIndexFlag, *applet)
	if err != nil {
		log.Fatalf("[test] 启动 applet 失败: %v", err)
	}
	defer sess.Close()
	log.Printf("[test] applet 就绪，发测试请求")

	headers := map[string]string{"Content-Type": "application/json"}
	log.Printf("[test] proxy_request 使用 applet 会话鉴权")

	reqID := fmt.Sprintf("req_%d_probe", time.Now().UnixNano())
	pr := buildapp.ProxyRequest{
		RequestID:        reqID,
		RequestAttemptID: reqID + "_attempt_1_xx",
		Method:           "POST",
		Path:             "/v1beta/models/" + *model + ":generateContent",
		QueryParams:      map[string]string{},
		Headers:          headers,
		Body:             `{"contents":[{"role":"user","parts":[{"text":"Reply with exactly: PROBE_OK"}]}]}`,
		StreamingMode:    "fake",
		IsGenerative:     true,
	}
	ch, err := sess.Submit(pr)
	if err != nil {
		log.Fatalf("[test] 提交请求失败: %v", err)
	}
	defer sess.Done(reqID)

	log.Printf("[test] 等待 applet 响应（request_id=%s）...", reqID)
	dumpOnce := sync.OnceFunc(func() {
		time.Sleep(20 * time.Second)
		log.Printf("[test] ===== 20s 诊断 dump =====")
		sess.DumpAppletDiagnostics(context.Background())
	})
	dumpOnce()

	got := false
	timer := time.NewTimer(*requestTimeout)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-ch:
			dumpOnce()
			if !ok {
				log.Printf("[test] 通道关闭")
				return
			}
			switch msg.EventType {
			case "response_headers":
				log.Printf("[test] response_headers status=%d", msg.Status)
				got = true
			case "chunk":
				if msg.Data != "" {
					fmt.Printf("DATA: %s\n", msg.Data)
					got = true
				}
			case "error":
				log.Printf("[test] ERROR: %s", msg.Message)
				return
			case "stream_close":
				log.Printf("[test] stream_close，结束")
				return
			default:
				log.Printf("[test] 未知事件类型: %s", msg.EventType)
			}
		case <-timer.C:
			log.Printf("[test] %s 无响应，退出（got=%v）", requestTimeout.String(), got)
			log.Printf("[test] ===== 超时前诊断 dump =====")
			sess.DumpAppletDiagnostics(context.Background())
			sess.Done(reqID)
			return
		}
	}
}

// runManualMode -no-click 诊断模式：手动走完启动流程（注入脚本→导航→引导→激活→发请求），
// 导航前注入点击捕获器确保所有点击都被记录。发 proxy_request 后不启动自动点击，等用户手动点 Launch!。
func runManualMode(opts camoufoxnative.Options, ws *buildapp.Server, authIndex int, appletURL string, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// 1. 启动 Camoufox
	cam, err := camoufoxnative.StartSession(ctx, opts)
	if err != nil {
		log.Fatalf("[manual] start camoufox: %v", err)
	}
	defer cam.Close()

	// 2. 导航前注入 authIndex responder（和以前一样）
	if err := cam.AddInitScript(ctx, buildapp.AuthIndexResponderJS); err != nil {
		log.Fatalf("[manual] add authIndex script: %v", err)
	}

	// 3. 导航前注入点击捕获器——在页面加载时就生效
	if err := cam.AddInitScriptAll(ctx, `(function(){
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
			if (window.__clickLog.length > 50) window.__clickLog.shift();
		}, true);
	})();`); err != nil {
		log.Fatalf("[manual] add click capture: %v", err)
	}
	log.Printf("[manual] 脚本注入完毕（authIndex + clickCapture）")

	// 4. 导航
	if appletURL == "" {
		appletURL = buildapp.AppletURL
	}
	if err := cam.Navigate(ctx, appletURL); err != nil {
		log.Fatalf("[manual] navigate: %v", err)
	}
	log.Printf("[manual] navigated to %s", appletURL)
	time.Sleep(5 * time.Second)

	// 5. 引导点击（Skip/Next/Got it/Preview）—— 直接复用 buildapp 的点击逻辑
	type frameInfo struct {
		id  string
		url string
	}
	getFrames := func() []frameInfo {
		all, _ := cam.AllContexts(ctx)
		var frames []frameInfo
		for _, fc := range all {
			u, _ := cam.EvaluateStringInContext(ctx, fc, `location.href`)
			u = strings.TrimSpace(u)
			if u != "" && u != "about:blank" {
				frames = append(frames, frameInfo{id: fc, url: u})
			}
		}
		return frames
	}

	// 向所有帧注入 click capture（AddInitScript 只覆盖顶层，跨域 iframe 需手动注入）
	clickCaptureEval := `(function(){
		if (window.__clickLog) return 'already';
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
			if (window.__clickLog.length > 50) window.__clickLog.shift();
		}, true);
		return 'installed';
	})()`
	injectClickCapture := func() {
		// 1. 顶层 context
		res, err := cam.EvaluateString(ctx, clickCaptureEval)
		res = strings.TrimSpace(res)
		if res == "installed" {
			log.Printf("[manual] click capture → top-level OK")
		} else if err != nil {
			log.Printf("[manual] click capture → top-level err: %v", err)
		}
		// 2. 用 FindFrame 精确注入 run.app iframe
		runFrameID, err := cam.FindFrame(ctx, "run.app")
		if err != nil {
			log.Printf("[manual] FindFrame run.app err: %v", err)
		} else if runFrameID != "" {
			res2, err2 := cam.EvaluateStringInContext(ctx, runFrameID, clickCaptureEval)
			res2 = strings.TrimSpace(res2)
			if res2 == "installed" {
				log.Printf("[manual] click capture → run.app iframe OK (ctx=%s)", runFrameID[:min(len(runFrameID), 16)])
			} else {
				log.Printf("[manual] click capture → run.app iframe res=%q err=%v", res2, err2)
			}
		} else {
			log.Printf("[manual] click capture → run.app iframe not found")
		}
		// 3. 遍历所有帧兜底
		for _, f := range getFrames() {
			if strings.Contains(f.url, "run.app") || strings.Contains(f.url, "aistudio.google.com") {
				continue // 已处理
			}
			r, _ := cam.EvaluateStringInContext(ctx, f.id, clickCaptureEval)
			r = strings.TrimSpace(r)
			if r == "installed" {
				log.Printf("[manual] click capture → %s OK", f.url[:min(len(f.url), 50)])
			}
		}
	}
	// 首次注入
	time.Sleep(3 * time.Second)
	injectClickCapture()
	log.Printf("[manual] click capture 已注入所有帧")

	clickInFrame := func(fc string, labels []string) string {
		js := fmt.Sprintf(`(function(){
			const labels = %s;
			const norm = s => (s||'').trim().toLowerCase();
			const match = el => {
				const t = norm(el.innerText);
				if (!t || el.offsetParent === null) return false;
				return labels.some(l => {
					const nl = norm(l);
					if (t === nl) return true;
					if (nl.length >= 6 && t.includes(nl)) return true;
					return false;
				});
			};
			const fire = el => {
				try { for (const type of ['pointerdown','mousedown','pointerup','mouseup','click']) el.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true,view:window})); } catch(e){ el.click(); }
				return el.innerText.trim();
			};
			for (const sel of ['button','[role=button]']) {
				for (const el of document.querySelectorAll(sel)) {
					if (match(el)) return fire(el);
				}
			}
			for (const el of document.querySelectorAll('a')) {
				if (match(el)) return fire(el);
			}
			for (const el of document.querySelectorAll('div')) {
				if (match(el)) return fire(el);
			}
			return '';
		})()`, buildapp.MustJSON(labels))
		res, err := cam.EvaluateStringInContext(ctx, fc, js)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(res)
	}

	// 引导弹窗标签（覆盖所有已知弹窗类型）
	onboard := []string{"Skip", "Got it", "Continue to the app", "Allow", "Accept", "接受", "同意", "继续", "close", "开", "开始"}
	// Preview 标签（激活用）
	preview := []string{"Preview", "Run", "▶", "运行", "预览", "Update preview", "更新预览"}

	log.Printf("[manual] 开始点穿引导...")
	deadline := time.Now().Add(90 * time.Second)
	clickedOnboard := false
	previewClicked := false // 只点一次 Preview，避免反复切换
	injectTick := 0
	for time.Now().Before(deadline) {
		if ws.Ready(authIndex) {
			log.Printf("[manual] ✅ WS ready!")
			break
		}
		// 每 3 轮重新注入 click capture（iframe 可能后加载）
		injectTick++
		if injectTick%3 == 0 {
			injectClickCapture()
		}
		frames := getFrames()
		for _, f := range frames {
			if c := clickInFrame(f.id, onboard); c != "" {
				log.Printf("[manual] clicked onboard: %s in %s", c, f.url[:min(len(f.url), 60)])
				clickedOnboard = true
				time.Sleep(2 * time.Second)
				continue
			}
		}
		// 弹窗常在 Preview 点击之后才弹出，关闭它们会让 Angular 路由重置回 Code 视图。
		// 因此不能一次性闭锁，改以 URL 为判据：只要当前不在 Preview 视图就补点一次。
		if clickedOnboard {
			cur, _ := cam.EvaluateString(ctx, `window.location.href`)
			cur = strings.TrimSpace(cur)
			if !strings.Contains(cur, "showPreview=true") {
				for _, f := range frames {
					if c := clickInFrame(f.id, preview); c != "" {
						log.Printf("[manual] clicked preview: %s (url lacked showPreview)", c)
						previewClicked = true
						time.Sleep(2 * time.Second)
						break
					}
				}
			} else if !previewClicked {
				previewClicked = true
				log.Printf("[manual] already in Preview view")
			}
		}
		time.Sleep(3 * time.Second)
	}

	if !ws.Ready(authIndex) {
		log.Fatalf("[manual] ❌ WS not ready within 90s")
	}

	// 5b. WS ready 后，只清残留弹窗，不再点 Preview（第一个循环已正确激活）
	time.Sleep(2 * time.Second)
	{
		dismissed := 0
		for attempt := 0; attempt < 10; attempt++ {
			frames := getFrames()
			anyClicked := false
			for _, f := range frames {
				if c := clickInFrame(f.id, onboard); c != "" {
					log.Printf("[manual] post-WS dismiss dialog: %s in %s", c, f.url[:min(len(f.url), 60)])
					anyClicked = true
					dismissed++
					time.Sleep(1 * time.Second)
				}
			}
			if !anyClicked {
				break
			}
		}
		if dismissed > 0 {
			log.Printf("[manual] dismissed %d dialogs, waiting 2s for UI settle", dismissed)
			time.Sleep(2 * time.Second)
		}
	}
	// 注意：不再 post-WS 点 Preview，第一个循环已确保 active（post-WS 点击会 toggle off）
	// 等 run.app iframe 加载
	time.Sleep(5 * time.Second)

	// 5c. 验证当前 URL 是否切到 showPreview=true
	{
		urlJS := `window.location.href`
		currentURL, _ := cam.EvaluateString(ctx, urlJS)
		currentURL = strings.TrimSpace(currentURL)
		log.Printf("[manual] current URL: %s", currentURL)
		if !strings.Contains(currentURL, "showPreview=true") {
			log.Printf("[manual] ⚠ URL 仍在 Code 视图，再次点击 Preview")
			frames := getFrames()
			for _, f := range frames {
				if c := clickInFrame(f.id, preview); c != "" {
					log.Printf("[manual] retry preview click: %s", c)
				}
			}
			time.Sleep(3 * time.Second)
			currentURL2, _ := cam.EvaluateString(ctx, urlJS)
			log.Printf("[manual] URL after retry: %s", strings.TrimSpace(currentURL2))
		}
	}

	// 6. 确认 run.app iframe 状态 + 可见按钮
	frames := getFrames()
	for _, f := range frames {
		tail, _ := cam.EvaluateStringInContext(ctx, f.id, `(document.body ? document.body.innerText.replace(/\\s+/g,' ').slice(-200) : '<no body>')`)
		log.Printf("[manual] frame %s url=%s tail=%s", f.id[:min(len(f.id), 20)], f.url[:min(len(f.url), 80)], strings.TrimSpace(tail))
	}
	// 6b. 枚举 run.app iframe 内所有可见按钮/可点击元素（定位 Launch!）
	for _, f := range frames {
		if !strings.Contains(f.url, "run.app") {
			continue
		}
		btnsJS := `(function(){
			var items=[];
			document.querySelectorAll('button,[role=button],a,[role=link],[onclick]').forEach(function(el){
				if(el.offsetParent===null && getComputedStyle(el).display==='none') return;
				var rect=el.getBoundingClientRect();
				if(rect.width===0||rect.height===0) return;
				items.push({tag:el.tagName,text:(el.textContent||'').trim().slice(0,80),cls:(typeof el.className==='string'?el.className:'').slice(0,120),id:el.id||'',role:el.getAttribute&&el.getAttribute('role')||'',x:Math.round(rect.x),y:Math.round(rect.y),w:Math.round(rect.width),h:Math.round(rect.height)});
			});
			return JSON.stringify(items);
		})()`
		btnsRes, err := cam.EvaluateStringInContext(ctx, f.id, btnsJS)
		if err != nil {
			log.Printf("[manual] run.app buttons err: %v", err)
		} else {
			log.Printf("[manual] run.app visible buttons: %s", btnsRes)
		}
		// 也 dump iframe 尺寸
		sizeJS := `JSON.stringify({w:window.innerWidth,h:window.innerHeight,scrollY:window.scrollY})`
		sizeRes, _ := cam.EvaluateStringInContext(ctx, f.id, sizeJS)
		log.Printf("[manual] run.app window size: %s", sizeRes)
	}

	// 7. 发 proxy_request（不启动自动点击）
	sess := buildapp.NewSession(cam, ws, authIndex)
	// 注入 fetch/XHR/postMessage 取证（抓人工点 Launch! 后的真实凭证头）
	if evidence, err := sess.CollectFetchEvidence(ctx); err != nil {
		log.Printf("[manual] 取证注入失败: %v", err)
	} else {
		log.Printf("[manual] evidence initial: %s", evidence)
	}
	reqID := fmt.Sprintf("manual_%d", time.Now().UnixNano())
	pr := buildapp.ProxyRequest{
		RequestID:        reqID,
		RequestAttemptID: reqID + "_attempt_1",
		Method:           "POST",
		Path:             "/v1beta/models/" + model + ":generateContent",
		QueryParams:      map[string]string{},
		Headers:          map[string]string{"Content-Type": "application/json"},
		Body:             `{"contents":[{"role":"user","parts":[{"text":"Reply with exactly: PROBE_OK"}]}]}`,
		StreamingMode:    "fake",
		IsGenerative:     true,
	}
	ch, err := sess.SubmitNoAutoClick(pr)
	if err != nil {
		log.Fatalf("[manual] submit: %v", err)
	}
	log.Printf("[manual] ✅ proxy_request 已发送 — 请在 Camoufox 手动点 Launch!")
	log.Printf("[manual] 每 5s dump clickLog，Ctrl+C 退出")

	// 8. dump 循环：读 clickLog + 页面状态
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	dumpState := func() {
		// 每次 dump 前重新注入 click capture（iframe 可能重载）
		injectClickCapture()
		frames := getFrames()
		for _, f := range frames {
			cl, _ := cam.EvaluateStringInContext(ctx, f.id, `JSON.stringify((window.__clickLog||[]).slice(-5))`)
			cl = strings.TrimSpace(cl)
			tail, _ := cam.EvaluateStringInContext(ctx, f.id, `(document.body ? document.body.innerText.replace(/\\s+/g,' ').slice(-200) : '<no body>')`)
			tail = strings.TrimSpace(tail)
			if cl != "" && cl != "[]" && cl != "null" {
				log.Printf("[manual] clickLog(%s): %s", f.url[:min(len(f.url), 50)], cl)
			}
			if tail != "" {
				log.Printf("[manual] tail(%s): %s", f.url[:min(len(f.url), 50)], tail[:min(len(tail), 150)])
			}
		}
		// 特别关注 run.app iframe：枚举所有可见按钮+尺寸
		for _, f := range frames {
			if !strings.Contains(f.url, "run.app") {
				continue
			}
			btnsJS := `(function(){
				var items=[];
				document.querySelectorAll('button,[role=button],a,[onclick]').forEach(function(el){
					var rect=el.getBoundingClientRect();
					if(rect.width===0||rect.height===0) return;
					if(getComputedStyle(el).display==='none'||getComputedStyle(el).visibility==='hidden') return;
					items.push({tag:el.tagName,text:(el.textContent||'').trim().slice(0,80),cls:(typeof el.className==='string'?el.className:'').slice(0,120),id:el.id||'',x:Math.round(rect.x),y:Math.round(rect.y),w:Math.round(rect.width),h:Math.round(rect.height)});
				});
				return JSON.stringify(items);
			})()`
			btnsRes, _ := cam.EvaluateStringInContext(ctx, f.id, btnsJS)
			log.Printf("[manual] run.app buttons: %s", btnsRes)
			sizeRes, _ := cam.EvaluateStringInContext(ctx, f.id, `JSON.stringify({w:window.innerWidth,h:window.innerHeight})`)
			log.Printf("[manual] run.app size: %s", sizeRes)
			// dump body text 500 chars
			bodyText, _ := cam.EvaluateStringInContext(ctx, f.id, `(document.body?document.body.innerText.replace(/\\s+/g,' ').slice(0,500):'<no body>')`)
			log.Printf("[manual] run.app body: %s", strings.TrimSpace(bodyText))
		}
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[manual] 超时")
			dumpState()
			sess.Done(reqID)
			return
		case <-ticker.C:
			dumpState()
		case msg, ok := <-ch:
			if !ok {
				log.Printf("[manual] channel closed")
				return
			}
			switch msg.EventType {
			case "response_headers":
				log.Printf("[manual] 🎉 response_headers status=%d", msg.Status)
			case "chunk":
				log.Printf("[manual] 🎉 chunk: %s", msg.Data[:min(len(msg.Data), 200)])
			case "error":
				log.Printf("[manual] ❌ error: %s", msg.Message)
			case "stream_close":
				log.Printf("[manual] 🎉 stream_close — 保持窗口开着 60s 继续 dump clickLog")
				if evidence, err := sess.CollectFetchEvidence(ctx); err != nil {
					log.Printf("[manual] evidence 读取失败: %v", err)
				} else {
					log.Printf("[manual] EVIDENCE (人工点击后): %s", evidence)
				}
				sess.Done(reqID)
				// 不 return！继续 dump 60s 让用户看到 clickLog
				holdCtx, holdCancel := context.WithTimeout(context.Background(), 60*time.Second)
				holdTick := time.NewTicker(5 * time.Second)
				for {
					select {
					case <-holdCtx.Done():
						holdTick.Stop()
						holdCancel()
						return
					case <-holdTick.C:
						dumpState()
					}
				}
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
