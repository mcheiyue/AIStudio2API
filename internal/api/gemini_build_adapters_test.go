package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

type recordingStudio struct {
	mode           string
	countCalls     int
	buildCalls     int
	buildPaths     []string
	buildBodies    [][]byte
	buildAccount   string
	countTokensOut aistudio.TokenCount
	buildStatus    int
	buildResponse  []byte
}

func (s *recordingStudio) Models(context.Context) ([]aistudio.Model, error) {
	return nil, nil
}

func (s *recordingStudio) CountTokens(context.Context, aistudio.TokenCountRequest) (aistudio.TokenCount, error) {
	s.countCalls++
	if s.countTokensOut.InputTokens == 0 {
		s.countTokensOut.InputTokens = 7
	}
	return s.countTokensOut, nil
}

func (s *recordingStudio) Generate(context.Context, aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	ch := make(chan aistudio.Event)
	close(ch)
	return ch, nil
}

func (s *recordingStudio) AccountMode(string) string { return s.mode }

func (s *recordingStudio) ServeBuildApp(_ context.Context, rw http.ResponseWriter, r *http.Request, accountID string) error {
	s.buildCalls++
	s.buildAccount = accountID
	body, _ := io.ReadAll(r.Body)
	s.buildPaths = append(s.buildPaths, r.URL.Path)
	s.buildBodies = append(s.buildBodies, body)
	status := s.buildStatus
	if status == 0 {
		status = http.StatusOK
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	if len(s.buildResponse) > 0 {
		_, _ = rw.Write(s.buildResponse)
	}
	return nil
}

func (s *recordingStudio) ServeBuildAppEvents(context.Context, []byte, string, bool, string) (<-chan aistudio.Event, error) {
	ch := make(chan aistudio.Event)
	close(ch)
	return ch, nil
}

func geminiHandler(studio *recordingStudio) http.Handler {
	s := &server{service: studio, buildApp: studio}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1beta/models/{action}", s.handleGeminiAction)
	return mux
}

func postGemini(t *testing.T, h http.Handler, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1beta/models/"+action, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestHandleGeminiAction_playgroundCountTokens_doesNotUseBuild(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModePlayground}
	rec := postGemini(t, geminiHandler(studio), "gemini-2.5-flash:countTokens",
		`{"contents":[{"parts":[{"text":"hi"}]}],"accountID":"acc-pg"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["totalTokens"] != 7 {
		t.Fatalf("totalTokens = %v", got)
	}
	if studio.countCalls != 1 || studio.buildCalls != 0 {
		t.Fatalf("countCalls=%d buildCalls=%d", studio.countCalls, studio.buildCalls)
	}
}

func TestHandleGeminiAction_playgroundEmbedContent_keepsExistingError(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModePlayground}
	rec := postGemini(t, geminiHandler(studio), "text-embedding-004:embedContent",
		`{"content":{"parts":[{"text":"hi"}]},"accountID":"acc-pg"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "contents is required") {
		t.Fatalf("playground embed error changed: %s", rec.Body.String())
	}
	if studio.buildCalls != 0 || studio.countCalls != 0 {
		t.Fatalf("playground embed started upstream count=%d build=%d", studio.countCalls, studio.buildCalls)
	}
}

func TestHandleGeminiAction_buildEmbedContent_convertsToBatchAndSplitsResponse(t *testing.T) {
	studio := &recordingStudio{
		mode:          aistudio.AccountModeBuildApp,
		buildResponse: []byte(`{"embeddings":[{"values":[0.25,0.5]}],"usageMetadata":{"totalTokenCount":2}}`),
	}
	rec := postGemini(t, geminiHandler(studio), "text-embedding-004:embedContent",
		`{"content":{"parts":[{"text":"hi"}]},"accountID":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.buildCalls != 1 || studio.countCalls != 0 {
		t.Fatalf("count=%d build=%d", studio.countCalls, studio.buildCalls)
	}
	if studio.buildAccount != "acc-build" {
		t.Fatalf("account = %q", studio.buildAccount)
	}
	if studio.buildPaths[0] != "/v1beta/models/text-embedding-004:batchEmbedContents" {
		t.Fatalf("path = %q", studio.buildPaths[0])
	}
	var sent struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(studio.buildBodies[0], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Requests) != 1 || sent.Requests[0]["model"] != "models/text-embedding-004" {
		t.Fatalf("batch body = %s", studio.buildBodies[0])
	}
	if _, ok := sent.Requests[0]["accountID"]; ok {
		t.Fatal("accountID leaked")
	}
	var out struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		Embeddings []any `json:"embeddings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Embeddings) != 0 {
		t.Fatalf("client still saw batch embeddings: %s", rec.Body.String())
	}
	if len(out.Embedding.Values) != 2 || out.Embedding.Values[0] != 0.25 {
		t.Fatalf("embedding = %#v", out.Embedding)
	}
}

func TestHandleGeminiAction_buildCountTokens_usesBuildRelay(t *testing.T) {
	studio := &recordingStudio{
		mode:          aistudio.AccountModeBuildApp,
		buildResponse: []byte(`{"totalTokens":12}`),
	}
	rec := postGemini(t, geminiHandler(studio), "gemini-2.5-flash:countTokens",
		`{"contents":[{"parts":[{"text":"hi"}]}],"accountID":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.buildCalls != 1 || studio.countCalls != 0 {
		t.Fatalf("count=%d build=%d", studio.countCalls, studio.buildCalls)
	}
	if studio.buildPaths[0] != "/v1beta/models/gemini-2.5-flash:countTokens" {
		t.Fatalf("path = %q", studio.buildPaths[0])
	}
	if strings.Contains(string(studio.buildBodies[0]), "accountID") {
		t.Fatalf("accountID leaked: %s", studio.buildBodies[0])
	}
	if !strings.Contains(rec.Body.String(), `"totalTokens":12`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleGeminiAction_buildBatchEmbedContents_passthrough(t *testing.T) {
	studio := &recordingStudio{
		mode:          aistudio.AccountModeBuildApp,
		buildResponse: []byte(`{"embeddings":[{"values":[1]},{"values":[2]}]}`),
	}
	rec := postGemini(t, geminiHandler(studio), "text-embedding-004:batchEmbedContents",
		`{"requests":[{"model":"models/text-embedding-004","content":{"parts":[{"text":"a"}]}}],"accountID":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.buildPaths[0] != "/v1beta/models/text-embedding-004:batchEmbedContents" {
		t.Fatalf("path = %q", studio.buildPaths[0])
	}
	var out struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Embeddings) != 2 {
		t.Fatalf("embeddings = %#v", out.Embeddings)
	}
}

func TestHandleGeminiAction_buildNative_rejectsEmptyAndUnknownWithoutWorker(t *testing.T) {
	tests := []struct {
		name   string
		action string
		body   string
		want   string
	}{
		{"empty countTokens", "gemini-2.5-flash:countTokens", `{"contents":[],"accountID":"acc-build"}`, "contents is required"},
		{"empty embed", "text-embedding-004:embedContent", `{"accountID":"acc-build"}`, "content is required"},
		{"empty batch", "text-embedding-004:batchEmbedContents", `{"requests":[],"accountID":"acc-build"}`, "requests is required"},
		{"unknown method", "gemini-2.5-flash:predict", `{"contents":[{"parts":[{"text":"x"}]}],"accountID":"acc-build"}`, "unknown method: predict"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
			rec := postGemini(t, geminiHandler(studio), tt.action, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("body = %s want %q", rec.Body.String(), tt.want)
			}
			if studio.buildCalls != 0 {
				t.Fatalf("worker started: %d", studio.buildCalls)
			}
		})
	}
}

func TestHandleGeminiAction_unknownAccount_keepsPlaygroundCountTokens(t *testing.T) {
	studio := &recordingStudio{mode: ""}
	rec := postGemini(t, geminiHandler(studio), "gemini-2.5-flash:countTokens",
		`{"contents":[{"parts":[{"text":"hi"}]}],"accountID":"missing"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.countCalls != 1 || studio.buildCalls != 0 {
		t.Fatalf("count=%d build=%d", studio.countCalls, studio.buildCalls)
	}
}
