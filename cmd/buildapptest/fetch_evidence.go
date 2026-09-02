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

// runFetchEvidenceProbe 取证模式：WS ready 后注入 fetch/postMessage 记录，
// 点一次 Preview（已知可 resolve bootstrapChannel、触发 fetch 但 403），
// 发 proxy_request 后 25s 读取实际 fetch 参数，判断 Go 直连复刻可行性。
func runFetchEvidenceProbe(opts camoufoxnative.Options, ws *buildapp.Server, authIndex int, appletURL, model string, requestTimeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second+requestTimeout+30*time.Second)
	defer cancel()

	sess, err := buildapp.LaunchApplet(ctx, opts, ws, authIndex, appletURL)
	if err != nil {
		log.Printf("[ev] 启动 applet 失败: %v", err)
		return
	}
	defer sess.Close()

	initial, err := sess.CollectFetchEvidence(ctx)
	if err != nil {
		log.Printf("[ev] 注入取证失败: %v", err)
	} else {
		log.Printf("[ev] initial evidence: %s", initial)
	}

	// 不再点 Preview：LaunchApplet 已把视图切到 Preview（URL 含 showPreview=true），
	// 再点一次会 toggle 回 Code 视图，run.app iframe 随之不渲染。
	reqID := fmt.Sprintf("ev_%d", time.Now().UnixNano())
	request := buildapp.ProxyRequest{
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
	ch, err := sess.Submit(request)
	if err != nil {
		log.Printf("[ev] 提交 proxy_request 失败: %v", err)
		return
	}
	defer sess.Done(reqID)
	log.Printf("[ev] proxy_request 已发送（请求期自动点 Launch!），25s 后收集 fetch 证据")

	time.Sleep(25 * time.Second)
	evidence, err := sess.CollectFetchEvidence(ctx)
	if err != nil {
		log.Printf("[ev] 收集证据失败: %v", err)
	} else {
		log.Printf("[ev] EVIDENCE: %s", evidence)
	}

	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			log.Printf("[ev] 60s 内未收到 applet 响应，退出")
			return
		case msg, ok := <-ch:
			if !ok {
				log.Printf("[ev] applet 通道关闭")
				return
			}
			switch msg.EventType {
			case "response_headers":
				log.Printf("[ev] response_headers status=%d", msg.Status)
			case "chunk":
				data := msg.Data
				if len(data) > 200 {
					data = data[:200]
				}
				log.Printf("[ev] chunk: %s", data)
			case "error":
				log.Printf("[ev] applet error: %s", msg.Message)
				return
			case "stream_close":
				log.Printf("[ev] stream_close")
				return
			default:
				log.Printf("[ev] unknown event: %s", msg.EventType)
			}
		}
	}
}

var _ = strings.TrimSpace
