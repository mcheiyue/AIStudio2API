package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/buildapp"
)

func uploadHandler(studio *recordingStudio) http.Handler {
	s := &server{service: studio, buildApp: studio}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/files", s.handleOpenAIFileUpload)
	return mux
}

func multipartUploadRequest(t *testing.T, path string, filename, content, accountID, purpose string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("account_id", accountID); err != nil {
		t.Fatal(err)
	}
	if purpose != "" {
		if err := writer.WriteField("purpose", purpose); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &buf)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	return r
}

func TestHandleOpenAIFileUpload_buildAccount_relaysExactBytes(t *testing.T) {
	const content = "PROBE-UPLOAD-BYTES"
	studio := &recordingStudio{
		mode:          aistudio.AccountModeBuildApp,
		buildResponse: []byte(`{"file":{"name":"files/abc123","uri":"https://generativelanguage.googleapis.com/v1beta/files/abc123","displayName":"probe.txt","mimeType":"text/plain","sizeBytes":"18","createTime":"2026-09-05T00:00:00Z"}}`),
	}
	r := multipartUploadRequest(t, "/v1/files", "probe.txt", content, "acc-build", "batch")
	rec := httptest.NewRecorder()
	uploadHandler(studio).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.buildCalls != 1 || studio.buildAccount != "acc-build" {
		t.Fatalf("calls=%d account=%q", studio.buildCalls, studio.buildAccount)
	}
	if studio.buildPaths[0] != "/upload/v1beta/files" {
		t.Fatalf("path = %q", studio.buildPaths[0])
	}
	if !strings.Contains(studio.buildContentType, "multipart/related") {
		t.Fatalf("content type = %q", studio.buildContentType)
	}
	if studio.buildContentLength != int64(len(studio.buildBodies[0])) {
		t.Fatalf("content length %d != body %d", studio.buildContentLength, len(studio.buildBodies[0]))
	}
	// buildapp 上传路径必须走 body_b64（二进制安全），还原字节必须与原始文件一致。
	proxy, err := buildapp.EncodeProxyRequest(func() *http.Request {
		r2 := httptest.NewRequest(http.MethodPost, studio.buildPaths[0], bytes.NewReader(studio.buildBodies[0]))
		r2.Header.Set("Content-Type", studio.buildContentType)
		return r2
	}(), studio.buildBodies[0])
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := buildapp.DecodeProxyPayload(proxy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(decoded, []byte(content)) {
		t.Fatalf("relay body lost file bytes: %d bytes decoded", len(decoded))
	}
	var out openAIFileObject
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "files/abc123" || out.Filename != "probe.txt" || out.Purpose != "batch" || out.Status != "processed" || out.Bytes != 18 {
		t.Fatalf("openai file object = %#v", out)
	}
}

func TestHandleOpenAIFileUpload_buildAccount_oversizeRejectedBeforeWorker(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
	oversize := strings.Repeat("a", buildapp.MaxRelayBodyBytes+1)
	r := multipartUploadRequest(t, "/v1/files", "big.bin", oversize, "acc-build", "batch")
	rec := httptest.NewRecorder()
	uploadHandler(studio).ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "32 MB relay bound") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if studio.buildCalls != 0 {
		t.Fatalf("worker started: %d", studio.buildCalls)
	}
}

func TestHandleOpenAIFileUpload_playgroundAccount_keepsExistingFlow(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModePlayground}
	r := multipartUploadRequest(t, "/v1/files", "probe.txt", "PROBE", "acc-pg", "batch")
	rec := httptest.NewRecorder()
	uploadHandler(studio).ServeHTTP(rec, r)
	// Playground 上传依赖真实 FileService，stub 未实现时保持既有 "unavailable" 错误路径。
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "file upload is unavailable") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if studio.buildCalls != 0 {
		t.Fatalf("worker started: %d", studio.buildCalls)
	}
}
