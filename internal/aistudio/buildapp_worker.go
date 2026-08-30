package aistudio

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/buildapp"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

// buildAppHeadless 返回 Camoufox 是否无头启动。默认 true（适配服务器部署）；
// 本地 GUI 调试可设 BUILDAPP_HEADLESS=false 观感浏览器行为。
func buildAppHeadless() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("BUILDAPP_HEADLESS")))
	if v == "false" || v == "0" {
		return false
	}
	return true
}

// BuildAppWorker 持有单个 buildapp 模式账号的 WS 中继 + applet 浏览器会话。
// 本质是把账号的原始 HTTP 请求经 applet 中继到 generativelanguage（反向代理）。
type BuildAppWorker struct {
	server    *buildapp.Server
	transport *buildapp.Transport
	state     atomic.Value // 存 string：idle/warming/ready/error，由 NewBuildAppWorker 设置
}

// NewBuildAppWorker 启动账号的 Build App 中继：起 WS 服务 + Camoufox applet 会话。
// storageState/appletURL 为该账号专属；addr 为本 worker 独占的 WS 监听地址（每账号一个端口）。
func NewBuildAppWorker(storageState, camoufoxPath, appletURL, addr string) (*BuildAppWorker, error) {
	srv := buildapp.NewServer()
	w := &BuildAppWorker{server: srv}
	w.state.Store("warming")
	srv.SetHooks(
		func(i int) { log.Printf("[buildapp] worker applet connected authIndex=%d", i) },
		func(i int) { log.Printf("[buildapp] worker applet disconnected authIndex=%d", i) },
	)
	go func() {
		if err := srv.Start(addr); err != nil {
			log.Printf("[buildapp] worker WS error: %v", err)
		}
	}()
	time.Sleep(500 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Second)
	defer cancel()
	opts := camoufoxnative.Options{
		ExecutablePath:   camoufoxPath,
		StorageStatePath: storageState,
		Locale:           "en-US",
		Timezone:         "America/New_York",
		Headless:         buildAppHeadless(),
		Log:              os.Stderr,
		// Camoufox 是独立进程，不会自动继承 GUI 浏览器的链式 HY2 代理；
		// 不设则直连 Google 被重置（NS_ERROR_NET_RESET / 300s 无回包）。
		// 运行时通过 BUILDAPP_PROXY 指定本地 Clash mixed 端口，如 socks5://127.0.0.1:7897。
		Proxy: os.Getenv("BUILDAPP_PROXY"),
		// applet 连本机 WS 中继（ws://127.0.0.1:9998）必须绕过代理，否则会被塞进 Clash 而连不上。
		ProxyBypass: "127.0.0.1,localhost",
	}
	if _, err := buildapp.LaunchApplet(ctx, opts, srv, 0, appletURL); err != nil {
		srv.Stop()
		return nil, err
	}
	transport := buildapp.NewTransport(srv, 0, "")
	w.transport = transport
	w.state.Store("ready")
	return w, nil
}

// State 返回 worker 当前就绪态：warming（创建中）/ready（applet 已连中继）。
// 不存在的 worker 由 AccountPool.BuildAppWorkerState 返回 idle。
func (w *BuildAppWorker) State() string {
	if v, ok := w.state.Load().(string); ok {
		return v
	}
	return "idle"
}

// ServeHTTP 把原始 HTTP 请求经 applet 中继到 generativelanguage。
func (w *BuildAppWorker) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(rw, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	reqID, ch, err := w.transport.SubmitRequest(r, body)
	if err != nil {
		http.Error(rw, "submit: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.transport.PumpTo(rw, ch, reqID)
}

// Close 关闭中继与浏览器会话。
func (w *BuildAppWorker) Close() error {
	w.server.Stop()
	return nil
}
