package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/buildapp"
	"github.com/Mag1cFall/AIStudio2API/internal/camoufoxnative"
)

func main() {
	storageState := flag.String("storage-state", "", "2267 storage-state.json 路径")
	camoufox := flag.String("camoufox", "", "camoufox.exe 路径")
	authIndex := flag.Int("authindex", 0, "WS authIndex")
	addr := flag.String("addr", ":9998", "WS 监听地址")
	model := flag.String("model", "gemini-2.5-flash", "测试模型")
	apiKey := flag.String("apikey", "", "2267 的 AI Studio API key（注入 x-goog-api-key 头）；为空则不带凭证")
	applet := flag.String("applet", "", "覆盖默认 applet URL（如 fork 自 cab9ab6c 的 2267 自有 app）")
	flag.Parse()

	if *storageState == "" || *camoufox == "" {
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

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	opts := camoufoxnative.Options{
		ExecutablePath:   *camoufox,
		StorageStatePath: *storageState,
		Locale:           "en-US",
		Timezone:         "America/New_York",
		Proxy:            os.Getenv("BUILDAPP_PROXY"),
		ProxyBypass:      "127.0.0.1,localhost",
		Headless:         false,
		Log:              os.Stderr,
		StartupProgress: func(s camoufoxnative.StartupStage) {
			log.Printf("[test] startup stage: %s", s)
		},
	}
	sess, err := buildapp.LaunchApplet(ctx, opts, ws, *authIndex, *applet)
	if err != nil {
		log.Fatalf("[test] 启动 applet 失败: %v", err)
	}
	defer sess.Close()
	log.Printf("[test] applet 就绪，发测试请求")

	// 凭证来源优先级：-apikey 显式 key > 会话 gapi OAuth Bearer（若可取）> 无凭证（预期 applet 零回包）
	headers := map[string]string{"Content-Type": "application/json"}
	if *apiKey != "" {
		headers["x-goog-api-key"] = *apiKey
		log.Printf("[test] 使用 -apikey 注入 x-goog-api-key 头")
	} else if raw := sess.TryGetGapiToken(ctx); raw != "" && raw != "token_null" && !strings.HasPrefix(raw, "no_") && !strings.HasPrefix(raw, "err:") {
		var bearer string
		if strings.HasPrefix(raw, "ya29") {
			bearer = raw
		} else if strings.Contains(raw, "access_token") {
			var m struct {
				AccessToken string `json:"access_token"`
			}
			if json.Unmarshal([]byte(raw), &m) == nil && m.AccessToken != "" {
				bearer = m.AccessToken
			}
		}
		if bearer == "" {
			bearer = raw
		}
		preview := bearer
		if len(bearer) > 12 {
			preview = bearer[:12]
		}
		log.Printf("[test] 取到 gapi token（前12位）: %s...", preview)
		headers["Authorization"] = "Bearer " + bearer
	} else {
		log.Printf("[test] 未取到凭证（%s），proxy_request 将不带凭证（预期 applet 零回包）", raw)
	}

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
	got := false
	timeout := time.After(45 * time.Second)
	for {
		select {
		case msg, ok := <-ch:
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
		case <-timeout:
			log.Printf("[test] 45s 无响应，退出（got=%v）", got)
			sess.Done(reqID)
			return
		}
	}
}
