package aistudio

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/buildapp"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

// BuildAppWorker 持有单个 buildapp 模式账号的 WS 中继 + applet 浏览器会话。
// 本质是把账号的原始 HTTP 请求经 applet 中继到 generativelanguage（反向代理）。
type BuildAppWorker struct {
	server    *buildapp.Server
	transport *buildapp.Transport
}

// NewBuildAppWorker 启动账号的 Build App 中继：起 WS 服务 + Camoufox applet 会话。
// storageState/appletURL 为该账号专属；addr 为本 worker 独占的 WS 监听地址（每账号一个端口）。
func NewBuildAppWorker(storageState, camoufoxPath, appletURL, addr string) (*BuildAppWorker, error) {
	srv := buildapp.NewServer()
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
		Headless:         false,
		Log:              os.Stderr,
	}
	if _, err := buildapp.LaunchApplet(ctx, opts, srv, 0, appletURL); err != nil {
		srv.Stop()
		return nil, err
	}
	transport := buildapp.NewTransport(srv, 0, "")
	return &BuildAppWorker{server: srv, transport: transport}, nil
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
