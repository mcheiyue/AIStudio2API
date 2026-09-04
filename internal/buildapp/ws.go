package buildapp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// ProxyRequest 是服务端发给 applet 的 proxy_request 消息（字段与 iBUHub 对齐）。
// 注意：只有 path + query_params，无完整 url；applet 自行拼
// https://generativelanguage.googleapis.com{path} 并发请求，凭证由 Build App 运行时注入。
type ProxyRequest struct {
	EventType         string            `json:"event_type"` // 固定 "proxy_request"
	RequestID         string            `json:"request_id"`
	RequestAttemptID  string            `json:"request_attempt_id"`
	Method            string            `json:"method"`
	Path              string            `json:"path"`
	QueryParams       map[string]string `json:"query_params"`
	Headers           map[string]string `json:"headers"`
	Body              string            `json:"body"`               // 现有 generateContent 的 JSON 文本
	BodyB64           string            `json:"body_b64,omitempty"` // 二进制上传，与 iBUHub body_b64 对齐
	StreamingMode     string            `json:"streaming_mode"`     // "real" | "fake" | ""
	IsGenerative      bool              `json:"is_generative"`
	ResponseTransform interface{}       `json:"response_transform"`
}

// AppletMessage 是 applet 回传的消息（response_headers / chunk / error / stream_close）。
// 所有字段按需可选，统一解析后由 transport 按 EventType 处理。
type AppletMessage struct {
	EventType string            `json:"event_type"`
	RequestID string            `json:"request_id"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Data      string            `json:"data"`
	Message   string            `json:"message"`
}

// Server 是 Build App 的本地 WS 中继服务端（等价于 iBUHub ProxyServerSystem）。
// applet（cab9ab6c，Google 写死）连接 ws://127.0.0.1:9998?authIndex=N，
// 服务端按 authIndex 注册连接，收到用户请求时转 proxy_request，applet 回 response_headers/chunk/stream_close。
type Server struct {
	upgrader websocket.Upgrader

	srv         *http.Server
	mu          sync.RWMutex
	conns       map[int]*websocket.Conn // authIndex -> conn
	pending     map[string]chan AppletMessage
	pendingMu   sync.Mutex
	onConnected func(authIndex int)
	onClosed    func(authIndex int)
}

func NewServer() *Server {
	return &Server{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		conns:   make(map[int]*websocket.Conn),
		pending: make(map[string]chan AppletMessage),
	}
}

// SetHooks 设置连接生命周期回调（用于 session 就绪判断）。
func (s *Server) SetHooks(onConnected, onClosed func(authIndex int)) {
	s.onConnected = onConnected
	s.onClosed = onClosed
}

// Start 在 addr 上起 WS 服务（如 :9998）。阻塞。
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/__ext_alive", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[buildapp] EXTENSION ALIVE from %s", r.RemoteAddr)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/__ext_log", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		log.Printf("[buildapp] EXT LOG: %s", string(body))
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", s.handleWS)
	s.srv = &http.Server{Addr: addr, Handler: mux}
	log.Printf("[buildapp] WS server listening on ws://%s", addr)
	return s.srv.ListenAndServe()
}

// Stop 关闭 WS 服务。
func (s *Server) Stop() {
	if s.srv != nil {
		_ = s.srv.Close()
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	authIndex := -1
	if v := q.Get("authIndex"); v != "" {
		if _, err := fmt.Sscan(v, &authIndex); err != nil {
			authIndex = -1
		}
	}
	if authIndex < 0 {
		log.Printf("[buildapp] reject WS: invalid authIndex=%q", q.Get("authIndex"))
		http.Error(w, "invalid authIndex", http.StatusBadRequest)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[buildapp] WS upgrade failed: %v", err)
		return
	}
	s.mu.Lock()
	s.conns[authIndex] = conn
	s.mu.Unlock()
	log.Printf("[buildapp] applet connected authIndex=%d", authIndex)
	if s.onConnected != nil {
		s.onConnected(authIndex)
	}

	defer func() {
		s.mu.Lock()
		delete(s.conns, authIndex)
		s.mu.Unlock()
		conn.Close()
		log.Printf("[buildapp] applet disconnected authIndex=%d", authIndex)
		if s.onClosed != nil {
			s.onClosed(authIndex)
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		log.Printf("[buildapp] RAW <- applet (%d bytes): %.300s", len(data), string(data))
		var msg AppletMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[buildapp] bad message from applet: %v", err)
			continue
		}
		if msg.RequestID == "" {
			continue
		}
		s.pendingMu.Lock()
		ch, ok := s.pending[msg.RequestID]
		s.pendingMu.Unlock()
		if !ok {
			log.Printf("[buildapp] stray message for unknown request_id=%s", msg.RequestID)
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}

// Ready 返回某 authIndex 是否已有 applet 连接。
func (s *Server) Ready(authIndex int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.conns[authIndex]
	return ok
}

// Submit 把 proxy_request 发给指定 authIndex 的 applet，返回该请求的响应消息通道。
// 调用方负责在收到 stream_close / error 后调用 Done(requestID) 清理。
func (s *Server) Submit(authIndex int, req ProxyRequest) (<-chan AppletMessage, error) {
	s.mu.RLock()
	conn, ok := s.conns[authIndex]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no applet connection for authIndex=%d", authIndex)
	}
	req.EventType = "proxy_request"
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	log.Printf("[buildapp] sending proxy_request bytes: %s", string(b))
	s.pendingMu.Lock()
	ch := make(chan AppletMessage, 64)
	s.pending[req.RequestID] = ch
	s.pendingMu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, req.RequestID)
		s.pendingMu.Unlock()
		return nil, err
	}
	return ch, nil
}

// Done 清理某请求的待收队列。
func (s *Server) Done(requestID string) {
	s.pendingMu.Lock()
	delete(s.pending, requestID)
	s.pendingMu.Unlock()
}
