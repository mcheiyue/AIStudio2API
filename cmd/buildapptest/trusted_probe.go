package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/buildapp"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

func runTrustedClickProbe(opts camoufoxnative.Options, ws *buildapp.Server, authIndex int, appletURL, model string, requestTimeout time.Duration, osInput bool, osClick bool, toolTest bool) {
	probeTimeout := 150*time.Second + 20*time.Second + 15*time.Second + requestTimeout + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	sess, err := buildapp.LaunchApplet(ctx, opts, ws, authIndex, appletURL)
	if err != nil {
		log.Printf("[trusted] 启动 applet 失败: %v", err)
		return
	}
	defer sess.Close()

	reqID := fmt.Sprintf("trusted_%d", time.Now().UnixNano())
	body := `{"contents":[{"role":"user","parts":[{"text":"Reply with exactly: PROBE_OK"}]}]}`
	if toolTest {
		body = `{"contents":[{"role":"user","parts":[{"text":"What is the weather in Beijing? Use the get_weather tool to answer."}]}],"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get current weather for a city","parameters":{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}}}`
	}
	request := buildapp.ProxyRequest{
		RequestID:        reqID,
		RequestAttemptID: reqID + "_attempt_1",
		Method:           "POST",
		Path:             "/v1beta/models/" + model + ":generateContent",
		QueryParams:      map[string]string{},
		Headers:          map[string]string{"Content-Type": "application/json"},
		Body:             body,
		StreamingMode:    "fake",
		IsGenerative:     true,
	}
	log.Printf("[trusted] Applet WS ready，发送 proxy_request；Launch! 将在 App 开始处理后出现")
	ch, err := sess.SubmitNoAutoClick(request)
	if err != nil {
		log.Printf("[trusted] 提交 proxy_request 失败: %v", err)
		return
	}
	defer sess.Done(reqID)
	log.Printf("[trusted] proxy_request 已发送，开始等待 App 内部出现 Launch!")

	if err := sess.RestoreClientWindow(ctx); err != nil {
		log.Printf("[trusted] 恢复 client window 失败: %v", err)
	} else {
		log.Printf("[trusted] client window 已恢复（原始值见 stderr [camoufox] clientWindows）")
	}

	if err := waitForAppletRequest(ctx, sess, request.Method, request.Path, 60*time.Second); err != nil {
		log.Printf("[trusted] App 内部未出现请求日志（期望图 2: %s）: %v", requestMarker(request.Method, request.Path), err)
		return
	}
	log.Printf("[trusted] App 内部已显示请求日志，开始等待 Launch! 覆盖层")

	hit, err := waitForLaunch(ctx, sess, 60*time.Second)
	if err != nil {
		log.Printf("[trusted] 点击前未找到 Launch!: %v", err)
		return
	}
	log.Printf("[trusted] 点击前 LaunchHit: %+v", hit)
	sharedID, err := waitForLaunchNode(ctx, sess, 5*time.Second)
	if err != nil {
		log.Printf("[trusted] Launch! DOM 节点未就绪: %v", err)
		return
	}
	log.Printf("[trusted] 点击前 Launch! sharedId=%s", sharedID)

	// 装 clickLog（记录 isTrusted），让可信点击自身被记录，便于与人工点击逐字段对比。
	if summary, err := sess.EvalAllContexts(ctx, trustedClickCaptureJS); err != nil {
		log.Printf("[trusted] 安装 clickLog 失败: %v", err)
	} else {
		log.Printf("[trusted] clickLog 安装: %s", strings.ReplaceAll(strings.TrimSpace(summary), "\n", " | "))
	}

	// OS click mode: Go 内置 OS 级真实鼠标点击 Launch!（等价人工点击）。
	// 循环定位并点击，直到 applet 回包（waitTrustedResponse 结束）。
	if osClick {
		log.Printf("[trusted] OS-CLICK MODE: Launch! overlay expected; auto-clicking via OS input...")
		clickCtx, stopClick := context.WithCancel(ctx)
		defer stopClick()
		go func() {
			ticker := time.NewTicker(1200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-clickCtx.Done():
					return
				case <-ticker.C:
					clicked, err := sess.ClickLaunch()
					if err != nil {
						log.Printf("[trusted] os-click err: %v", err)
						continue
					}
					if clicked {
						log.Printf("[trusted] os-click Launch! OK")
						return
					}
				}
			}
		}()
		waitTrustedResponse(ctx, ch, requestTimeout, toolTest)
		return
	}

	// OS input mode: Launch! overlay is up. Skip BiDi click entirely; wait for an
	// external OS-level input (e.g. Windows SendInput) which Firefox treats as a real
	// user gesture (establishes user activation). Dump Launch! viewport rect + client
	// window screen rect periodically so an external script can compute the screen
	// coordinates of the Launch! button and perform a real mouse click.
	if osInput {
		log.Printf("[trusted] OS-INPUT MODE: Launch! overlay is up; waiting for external OS-level keyboard/mouse input...")
		actCtx, stopAct := context.WithCancel(ctx)
		defer stopAct()
		go func() {
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-actCtx.Done():
					return
				case <-ticker.C:
					if act, err := sess.EvalAllContexts(ctx, userActivationJS); err == nil {
						log.Printf("[trusted] userActivation: %s", strings.ReplaceAll(strings.TrimSpace(act), "\n", " | "))
					}
					if h, err := sess.LocateLaunch(ctx); err == nil && h.Found {
						log.Printf("[trusted] LAUNCH_VIEWPORT cx=%0.0f cy=%0.0f w=%0.0f h=%0.0f", h.CX, h.CY, h.W, h.H)
					}
					if wx, wy, ww, wh, err := sess.ClientWindowRect(ctx); err == nil {
						log.Printf("[trusted] WIN_RECT x=%0.0f y=%0.0f w=%0.0f h=%0.0f", wx, wy, ww, wh)
					} else {
						log.Printf("[trusted] WIN_RECT err: %v", err)
					}
				}
			}
		}()
		waitTrustedResponse(ctx, ch, requestTimeout, toolTest)
		return
	}

	if err := sess.TrustedClickLaunch(ctx, sharedID); err != nil {
		log.Printf("[trusted] 真实点击失败: %v", err)
		return
	}
	log.Printf("[trusted] input.performActions 已成功返回")

	time.Sleep(3 * time.Second)
	if dump, err := sess.EvalAllContexts(ctx, `JSON.stringify(window.__trustedClicks || null)`); err != nil {
		log.Printf("[trusted] 读取 clickLog 失败: %v", err)
	} else {
		log.Printf("[trusted] 点击后 clickLog: %s", strings.ReplaceAll(strings.TrimSpace(dump), "\n", " | "))
	}
	if act, err := sess.EvalAllContexts(ctx, userActivationJS); err == nil {
		log.Printf("[trusted] userActivation: %s", strings.ReplaceAll(strings.TrimSpace(act), "\n", " | "))
	}

	// 单次点击已验证 trusted=true；重试点击那轮曾拿到真实 403，故这里保留重试，
	// 用同一轮同时判定「重试是否必要」与「403 是否可复现」。
	clickCtx, stopClicks := context.WithCancel(ctx)
	defer stopClicks()
	go retryTrustedClick(clickCtx, sess)

	// 覆盖层是否消失不是成功判据——回包才是。与人手一致只点一次，随后等回包。
	waitTrustedResponse(ctx, ch, requestTimeout, toolTest)
}

// trustedClickCaptureJS 记录每次 click 的 isTrusted、坐标与元素链，用于判定
// input.performActions 派发的事件是否被页面视为真实用户手势。
const trustedClickCaptureJS = `(function(){
	if (window.__trustedClicks) return 'already';
	window.__trustedClicks = [];
	['pointerdown','click'].forEach(function(type){
		addEventListener(type, function(ev){
			var el = ev.target;
			var chain = [];
			for (var n = el; n && chain.length < 5; n = n.parentElement) {
				var info = n.tagName.toLowerCase();
				if (n.className && typeof n.className === 'string') info += '.' + n.className.split(' ').slice(0,2).join('.');
				chain.push(info);
			}
			window.__trustedClicks.push({type: type, trusted: ev.isTrusted, x: ev.clientX, y: ev.clientY, tag: el.tagName, text: (el.textContent||'').trim().slice(0,40), chain: chain});
			if (window.__trustedClicks.length > 20) window.__trustedClicks.shift();
		}, true);
	});
	return 'installed';
})()`

// userActivationJS 读取 navigator.userActivation，判断可信点击是否给页面留下了
// sticky/transient 用户激活状态——Google 运行时可能以此为放行 bootstrapChannel 的条件。
const userActivationJS = `JSON.stringify(navigator.userActivation ? {sticky: navigator.userActivation.hasBeenActive, transient: navigator.userActivation.isActive} : 'unsupported')`

// retryTrustedClick 在等待回包期间持续用可信输入点击 Launch!，直到上下文取消。
func retryTrustedClick(ctx context.Context, sess *buildapp.Session) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sharedID, err := waitForLaunchNode(ctx, sess, 2*time.Second)
		if err != nil {
			continue
		}
		if err := sess.TrustedClickLaunch(ctx, sharedID); err != nil {
			log.Printf("[trusted] 重试真实点击失败: %v", err)
			continue
		}
		log.Printf("[trusted] 重试真实点击已发送 sharedId=%s", sharedID)
	}
}

func waitForLaunch(ctx context.Context, sess *buildapp.Session, timeout time.Duration) (buildapp.LaunchHit, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		hit, err := sess.LocateLaunch(ctx)
		if err == nil && hit.Found && hit.W > 1 && hit.H > 1 {
			return hit, nil
		}
		select {
		case <-ctx.Done():
			return buildapp.LaunchHit{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return buildapp.LaunchHit{}, fmt.Errorf("等待 Launch! 定位超时")
}

func waitForAppletRequest(ctx context.Context, sess *buildapp.Session, method, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		seen, err := sess.AppletShowsRequest(ctx, method, path)
		if err == nil && seen {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("等待 App 内部请求日志超时")
}

func requestMarker(method, path string) string {
	return fmt.Sprintf("Received request: %s %s", method, path)
}

func waitForLaunchNode(ctx context.Context, sess *buildapp.Session, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sharedID, err := sess.LocateLaunchNode(ctx)
		if err == nil && sharedID != "" {
			return sharedID, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("等待 Launch! DOM 节点超时")
}

func waitForLaunchGone(ctx context.Context, sess *buildapp.Session, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		hit, err := sess.LocateLaunch(ctx)
		if err == nil && (!hit.Found || hit.W <= 1 || hit.H <= 1) {
			log.Printf("[trusted] 点击后 LaunchHit: %+v", hit)
			return nil
		}
		if err != nil {
			log.Printf("[trusted] 点击后 LaunchHit 读取失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("Launch! 覆盖层 15s 内未消失")
}

func waitTrustedResponse(ctx context.Context, ch <-chan buildapp.AppletMessage, timeout time.Duration, toolTest bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	gotStatus := false
	gotProbe := false
	gotAny := false
	for {
		select {
		case <-ctx.Done():
			log.Printf("[trusted] 请求上下文结束（status=%v probe=%v data=%v）", gotStatus, gotProbe, gotAny)
			return
		case <-timer.C:
			log.Printf("[trusted] 请求超时（status=%v probe=%v data=%v）", gotStatus, gotProbe, gotAny)
			return
		case msg, ok := <-ch:
			if !ok {
				log.Printf("[trusted] applet 通道关闭（status=%v probe=%v data=%v）", gotStatus, gotProbe, gotAny)
				return
			}
			switch msg.EventType {
			case "response_headers":
				gotStatus = msg.Status == 200
				log.Printf("[trusted] response_headers status=%d", msg.Status)
			case "chunk":
				gotAny = gotAny || strings.TrimSpace(msg.Data) != ""
				gotProbe = gotProbe || containsProbeOK(msg.Data)
				if toolTest && len(msg.Data) > 0 {
					log.Printf("[trusted] chunk(data prefix): %s", msg.Data[:min(len(msg.Data), 400)])
				} else {
					log.Printf("[trusted] chunk contains PROBE_OK=%v", gotProbe)
				}
			case "error":
				log.Printf("[trusted] applet error: %s", msg.Message)
				return
			case "stream_close":
				log.Printf("[trusted] stream_close status=%v probe=%v data=%v", gotStatus, gotProbe, gotAny)
				if toolTest {
					log.Printf("[trusted] TOOL TEST RESULT: status=%v data=%v (functionCall 需人工核对 data 前缀)", gotStatus, gotAny)
				}
				return
			}
		}
	}
}

func containsProbeOK(data string) bool {
	return strings.Contains(data, "PROBE_OK")
}
