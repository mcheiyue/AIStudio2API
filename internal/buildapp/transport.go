package buildapp

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Transport 是 Build App 传输层：把用户（已转成 Gemini native 的）请求经 WS 中继给 applet，
// applet 借 Build App 运行时注入的凭证打 Google，结果流式回传。纯中继，Go 端不持有 key/token。
type Transport struct {
	ws        *Server
	authIndex int
	// APIKey 为账号级 AI Studio API key（2267 的 Pro 配额内），非空时注入 proxy_request 的
	// x-goog-api-key 头；applet 用此凭证调 generativelanguage。为空则依赖上游已带的 Authorization。
	APIKey string
}

func NewTransport(ws *Server, authIndex int, apiKey string) *Transport {
	return &Transport{ws: ws, authIndex: authIndex, APIKey: apiKey}
}

// SubmitRequest 把一次 HTTP 请求转为 proxy_request 并发给 applet，返回请求 ID 与响应消息通道。
// path 应为 Gemini native 路径（如 /v1beta/models/gemini-2.5-flash:generateContent）；
// 若上游带 /proxy 前缀则在此剥离（与 iBUHub _buildProxyRequest 一致）。
func (t *Transport) SubmitRequest(r *http.Request, body []byte) (string, <-chan AppletMessage, error) {
	path := strings.TrimPrefix(r.URL.Path, "/proxy")
	reqID := fmt.Sprintf("req_%d_%s", time.Now().UnixNano(), randString(6))
	headers := headerToMap(r.Header)
	// 必须剥掉客户端原始 Content-Length（含 accountID 时字节数与真实转发 body 不符，
	// 会让 applet 转给 Google 后 Google 一直等更多字节→300s 超时 504）。
	// Accept-Encoding 同理，会让 applet 期望压缩响应而解不出。
	delete(headers, "Content-Length")
	delete(headers, "Content-Length-Lower") // 防御性
	delete(headers, "Accept-Encoding")
	if t.APIKey != "" {
		headers["x-goog-api-key"] = t.APIKey
	}
	queryParams := queryToMap(r.URL.Query())
	// applet 用 2267 会话鉴权，不需要也不接受上游的 ?key=（无效 key 会让 Google 拒、且 applet 不回包）。
	// 透传给 Google 的 key 必须剥掉；同样 alt 由下方流式逻辑自行决定。
	delete(queryParams, "key")
	delete(queryParams, "alt")
	// iBUHub 约定：非流式 :generateContent 用 streaming_mode="fake"（applet 转成伪流一次性返回）；
	// 仅 :streamGenerateContent 用 "real" + alt=sse 走真正的 SSE。
	streamingMode := "fake"
	if strings.Contains(path, "streamGenerateContent") {
		streamingMode = "real"
		if _, ok := queryParams["alt"]; !ok {
			queryParams["alt"] = "sse"
		}
	}
	pr := ProxyRequest{
		RequestID:        reqID,
		RequestAttemptID: fmt.Sprintf("%s_attempt_1_%s", reqID, randString(4)),
		Method:           r.Method,
		Path:             path,
		QueryParams:      queryParams,
		Headers:          headers,
		Body:             string(body),
		StreamingMode:    streamingMode,
		IsGenerative:     strings.Contains(path, "generateContent"),
	}
	ch, err := t.ws.Submit(t.authIndex, pr)
	if err != nil {
		return "", nil, err
	}
	return reqID, ch, nil
}

// PumpTo 把 applet 回传的消息泵送到 HTTP 响应：response_headers 写状态+头，chunk 写数据，
// stream_close/error 结束；最后清理队列。
func (t *Transport) PumpTo(w http.ResponseWriter, ch <-chan AppletMessage, reqID string) {
	defer t.ws.Done(reqID)
	headersWritten := false
	for msg := range ch {
		switch msg.EventType {
		case "response_headers":
			if !headersWritten {
				if msg.Status > 0 {
					w.WriteHeader(msg.Status)
				}
				for k, v := range msg.Headers {
					w.Header().Add(k, v)
				}
				headersWritten = true
			}
		case "chunk":
			if !headersWritten {
				w.WriteHeader(http.StatusOK)
				headersWritten = true
			}
			if msg.Data != "" {
				_, _ = w.Write([]byte(msg.Data))
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		case "error":
			if !headersWritten {
				w.WriteHeader(http.StatusBadGateway)
				headersWritten = true
			}
			_, _ = w.Write([]byte(msg.Message))
			return
		case "stream_close":
			if !headersWritten {
				w.WriteHeader(http.StatusOK)
			}
			return
		}
	}
}

func queryToMap(q url.Values) map[string]string {
	m := make(map[string]string, len(q))
	for k, v := range q {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

func headerToMap(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

func randString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
