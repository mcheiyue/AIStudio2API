package buildapp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxRelayBodyBytes 是单次 proxy_request 解码后的 JSON/二进制 body 上限。
// WS 文本帧承载 JSON（二进制再套 base64）；避免无界缓冲。分片上传若需要更大再提高。
const MaxRelayBodyBytes = 32 << 20

var (
	ErrUnsupportedMethod = errors.New("buildapp: unsupported method")
	ErrUnsupportedPath   = errors.New("buildapp: unsupported path")
	ErrExternalURL       = errors.New("buildapp: external URL is not allowed")
	ErrMissingMIME       = errors.New("buildapp: missing Content-Type for binary body")
	ErrMalformedBodyB64  = errors.New("buildapp: malformed body_b64")
	ErrBodyTooLarge      = errors.New("buildapp: relay body exceeds size bound")
	ErrAmbiguousBody     = errors.New("buildapp: body and body_b64 are mutually exclusive")
)

// RelayError 是 relay 边界校验失败。调用方用 HTTPStatus 映射状态码。
type RelayError struct {
	err error
}

func (e *RelayError) Error() string {
	if e == nil || e.err == nil {
		return "buildapp: relay error"
	}
	return e.err.Error()
}

func (e *RelayError) Unwrap() error { return e.err }

func (e *RelayError) HTTPStatus() int {
	if e != nil && errors.Is(e.err, ErrBodyTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func newRelayError(sentinel error, detail string) error {
	if detail == "" {
		return &RelayError{err: sentinel}
	}
	return &RelayError{err: fmt.Errorf("%w: %s", sentinel, detail)}
}

// RelayHTTPStatus 把校验错误映射为 HTTP 状态；其余错误保持 502。
func RelayHTTPStatus(err error) int {
	var re *RelayError
	if errors.As(err, &re) {
		return re.HTTPStatus()
	}
	return http.StatusBadGateway
}

func newProxyRequest(r *http.Request, body []byte, apiKey string) (ProxyRequest, error) {
	if len(body) > MaxRelayBodyBytes {
		return ProxyRequest{}, newRelayError(ErrBodyTooLarge, strconv.Itoa(MaxRelayBodyBytes))
	}
	path := strings.TrimPrefix(r.URL.Path, "/proxy")
	if err := checkRelayPath(path); err != nil {
		return ProxyRequest{}, err
	}
	if !allowedRelayMethod(r.Method) {
		return ProxyRequest{}, newRelayError(ErrUnsupportedMethod, r.Method)
	}

	headers := headerToMap(r.Header)
	stripHopByHopHeaders(headers)
	if apiKey != "" {
		headers["x-goog-api-key"] = apiKey
	}

	queryParams := queryToMap(r.URL.Query())
	delete(queryParams, "key")
	delete(queryParams, "alt")

	streamingMode := "fake"
	if strings.Contains(path, "streamGenerateContent") {
		streamingMode = "real"
		if _, ok := queryParams["alt"]; !ok {
			queryParams["alt"] = "sse"
		}
	}

	contentType := r.Header.Get("Content-Type")
	pr := ProxyRequest{
		RequestID:     fmt.Sprintf("req_%d_%s", time.Now().UnixNano(), randString(6)),
		Method:        r.Method,
		Path:          path,
		QueryParams:   queryParams,
		Headers:       headers,
		StreamingMode: streamingMode,
		IsGenerative:  strings.Contains(path, "generateContent"),
	}
	pr.RequestAttemptID = fmt.Sprintf("%s_attempt_1_%s", pr.RequestID, randString(4))

	if useBinaryBody(path, contentType, body) {
		if strings.TrimSpace(contentType) == "" {
			return ProxyRequest{}, newRelayError(ErrMissingMIME, path)
		}
		pr.BodyB64 = base64.StdEncoding.EncodeToString(body)
		headers["content-type"] = contentType
	} else {
		pr.Body = string(body)
	}
	headers["Content-Length"] = strconv.Itoa(len(body))
	return pr, nil
}

func useBinaryBody(path, contentType string, body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if isUploadPath(path) {
		return true
	}
	return contentType != "" && !jsonContentType(contentType)
}

func isUploadPath(path string) bool {
	return strings.Contains(path, "/upload/")
}

func jsonContentType(ct string) bool {
	media, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.EqualFold(media, "application/json")
}

func allowedRelayMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func checkRelayPath(path string) error {
	if path == "" || strings.ContainsRune(path, 0) {
		return newRelayError(ErrUnsupportedPath, path)
	}
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return newRelayError(ErrExternalURL, path)
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return newRelayError(ErrUnsupportedPath, path)
	}
	if strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/upload/") {
		return nil
	}
	return newRelayError(ErrUnsupportedPath, path)
}

func stripHopByHopHeaders(headers map[string]string) {
	for k := range headers {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Accept-Encoding") {
			delete(headers, k)
		}
	}
}

func relayPayloadBytes(pr ProxyRequest) ([]byte, error) {
	if pr.Body != "" && pr.BodyB64 != "" {
		return nil, newRelayError(ErrAmbiguousBody, "")
	}
	if pr.BodyB64 == "" {
		return []byte(pr.Body), nil
	}
	decoded, err := base64.StdEncoding.DecodeString(pr.BodyB64)
	if err != nil {
		return nil, newRelayError(ErrMalformedBodyB64, err.Error())
	}
	return decoded, nil
}
