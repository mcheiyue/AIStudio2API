package buildapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func jsonGenerateRequest(body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent?key=secret&alt=json", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Content-Length", "99999")
	r.Header.Set("Accept-Encoding", "gzip")
	return r
}

func TestNewProxyRequest_JSONBody_preservesExactBytes(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	pr, err := newProxyRequest(jsonGenerateRequest(body), body, "")
	if err != nil {
		t.Fatalf("newProxyRequest() error = %v", err)
	}
	if pr.Body != string(body) {
		t.Fatalf("Body = %q, want exact JSON", pr.Body)
	}
	if pr.BodyB64 != "" {
		t.Fatalf("BodyB64 = %q, want empty for JSON", pr.BodyB64)
	}
	if pr.Path != "/v1beta/models/gemini-2.5-flash:generateContent" {
		t.Fatalf("Path = %q", pr.Path)
	}
	if pr.StreamingMode != "fake" || !pr.IsGenerative {
		t.Fatalf("streaming/generative = %q %v", pr.StreamingMode, pr.IsGenerative)
	}
	if _, ok := pr.QueryParams["key"]; ok {
		t.Fatal("query key must be stripped")
	}
	if pr.Headers["Content-Length"] != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q, want rebuilt size", pr.Headers["Content-Length"])
	}
	if pr.Headers["Content-Length"] == "99999" {
		t.Fatal("stale Content-Length was preserved")
	}
	if _, ok := pr.Headers["Accept-Encoding"]; ok {
		t.Fatal("Accept-Encoding must be stripped")
	}
	got, err := relayPayloadBytes(pr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("payload bytes = %q", got)
	}
}

func TestNewProxyRequest_JSONBody_stripsProxyPrefixAndSetsStreamMode(t *testing.T) {
	body := []byte(`{"contents":[]}`)
	r := httptest.NewRequest(http.MethodPost, "/proxy/v1beta/models/gemini-2.5-flash:streamGenerateContent", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	pr, err := newProxyRequest(r, body, "")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
		t.Fatalf("Path = %q", pr.Path)
	}
	if pr.StreamingMode != "real" || pr.QueryParams["alt"] != "sse" {
		t.Fatalf("stream mode = %q alt=%q", pr.StreamingMode, pr.QueryParams["alt"])
	}
	if pr.BodyB64 != "" {
		t.Fatal("JSON charset must stay on Body, not body_b64")
	}
}

func TestNewProxyRequest_binaryBody_roundtripsExactBytes(t *testing.T) {
	body := []byte{0x00, 0xff, 0xfe, 0x80, 'P', 'N', 'G'}
	r := httptest.NewRequest(http.MethodPost, "/upload/v1beta/files", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("Content-Length", "99999")
	pr, err := newProxyRequest(r, body, "")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Body != "" {
		t.Fatalf("Body = %q, want empty when body_b64 is set", pr.Body)
	}
	if pr.IsGenerative || pr.StreamingMode != "fake" {
		t.Fatalf("upload must be non-generative fake stream, got gen=%v mode=%q", pr.IsGenerative, pr.StreamingMode)
	}
	if pr.Headers["content-type"] != "application/octet-stream" {
		t.Fatalf("applet content-type = %q", pr.Headers["content-type"])
	}
	if pr.Headers["Content-Length"] != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q", pr.Headers["Content-Length"])
	}
	encoded, err := json.Marshal(pr)
	if err != nil {
		t.Fatal(err)
	}
	var wire ProxyRequest
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	got, err := relayPayloadBytes(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("decoded = %v, want %v", got, body)
	}
}

func TestNewProxyRequest_uploadJSONMetadata_usesBodyB64(t *testing.T) {
	body := []byte(`{"file":{"displayName":"a.txt"}}`)
	r := httptest.NewRequest(http.MethodPost, "/upload/v1beta/files", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	pr, err := newProxyRequest(r, body, "")
	if err != nil {
		t.Fatal(err)
	}
	if pr.BodyB64 == "" || pr.Body != "" {
		t.Fatalf("upload must use body_b64, body=%q b64=%q", pr.Body, pr.BodyB64)
	}
	got, err := relayPayloadBytes(pr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("decoded metadata = %q", got)
	}
}

func TestRelayPayloadBytes_rejectsMalformedBase64(t *testing.T) {
	_, err := relayPayloadBytes(ProxyRequest{BodyB64: "%%%not-base64%%%"})
	if !errors.Is(err, ErrMalformedBodyB64) {
		t.Fatalf("err = %v", err)
	}
	if RelayHTTPStatus(err) != http.StatusBadRequest {
		t.Fatalf("status = %d", RelayHTTPStatus(err))
	}
}

func TestRelayPayloadBytes_rejectsAmbiguousBody(t *testing.T) {
	_, err := relayPayloadBytes(ProxyRequest{Body: "{}", BodyB64: "e30="})
	if !errors.Is(err, ErrAmbiguousBody) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewProxyRequest_rejectsInvalidMethodPathAndMIME(t *testing.T) {
	jsonBody := []byte(`{}`)
	bin := []byte{0xff}
	tests := []struct {
		name    string
		method  string
		target  string
		ct      string
		body    []byte
		wantErr error
	}{
		{"trace", http.MethodTrace, "/v1beta/models/x:generateContent", "application/json", jsonBody, ErrUnsupportedMethod},
		{"connect", http.MethodConnect, "/v1beta/models/x:generateContent", "application/json", jsonBody, ErrUnsupportedMethod},
		{"admin path", http.MethodPost, "/admin/secret", "application/json", jsonBody, ErrUnsupportedPath},
		{"external url path", http.MethodPost, "/https://evil.example/v1beta/models/x:generateContent", "application/json", jsonBody, ErrExternalURL},
		{"scheme path", http.MethodPost, "http://evil.example/v1beta/models/x:generateContent", "application/json", jsonBody, ErrExternalURL},
		{"dot-dot", http.MethodPost, "/v1beta/../upload/files", "application/json", jsonBody, ErrUnsupportedPath},
		{"upload missing mime", http.MethodPost, "/upload/v1beta/files", "", bin, ErrMissingMIME},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1beta/models/x:generateContent", bytes.NewReader(tt.body))
			r.Method = tt.method
			r.URL.Path = tt.target
			if tt.ct != "" {
				r.Header.Set("Content-Type", tt.ct)
			}
			_, err := newProxyRequest(r, tt.body, "")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if RelayHTTPStatus(err) != http.StatusBadRequest {
				t.Fatalf("status = %d", RelayHTTPStatus(err))
			}
		})
	}
}

func TestNewProxyRequest_rejectsOversizedBody(t *testing.T) {
	body := make([]byte, MaxRelayBodyBytes+1)
	r := jsonGenerateRequest(body[:1])
	_, err := newProxyRequest(r, body, "")
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if RelayHTTPStatus(err) != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", RelayHTTPStatus(err))
	}
}

func TestSubmitRequest_rejectsExternalURLWithoutPendingLeak(t *testing.T) {
	srv := NewServer()
	tr := NewTransport(srv, 0, "")
	body := []byte(`{}`)
	r := httptest.NewRequest(http.MethodPost, "/https://evil.example/v1beta/models/x:generateContent", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	_, _, err := tr.SubmitRequest(r, body)
	if !errors.Is(err, ErrExternalURL) {
		t.Fatalf("err = %v", err)
	}
	srv.pendingMu.Lock()
	n := len(srv.pending)
	srv.pendingMu.Unlock()
	if n != 0 {
		t.Fatalf("pending queue leaked %d entries", n)
	}
}

func TestRelayHTTPStatus_untypedErrorIsBadGateway(t *testing.T) {
	if got := RelayHTTPStatus(errors.New("no applet connection")); got != http.StatusBadGateway {
		t.Fatalf("status = %d", got)
	}
}
