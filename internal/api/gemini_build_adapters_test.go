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
	mode               string
	countCalls         int
	buildCalls         int
	buildPaths         []string
	buildBodies        [][]byte
	buildAccount       string
	countTokensOut     aistudio.TokenCount
	buildStatus        int
	buildResponse      []byte
	buildContentLength int64
	buildContentType   string
	buildModels        []aistudio.BuildAppModel // nil 且 catalogOK=false 时不实现目录接口
	buildModelsErr     error
	catalogOK          bool
	catalogFetches     int
	sabCalls           int
	sabBody            []byte
	sabModel           string
	sabStream          bool
	sabAccount         string
	generateCalls      int
}

func (s *recordingStudio) BuildAppModels(context.Context, string) ([]aistudio.BuildAppModel, error) {
	s.catalogFetches++
	return s.buildModels, s.buildModelsErr
}

func (s *recordingStudio) BuildAppCatalogInfo(string) aistudio.BuildAppCatalogInfo {
	if s.buildModelsErr != nil {
		return aistudio.BuildAppCatalogInfo{Err: s.buildModelsErr}
	}
	return aistudio.BuildAppCatalogInfo{Size: len(s.buildModels)}
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
	s.generateCalls++
	ch := make(chan aistudio.Event, 2)
	ch <- aistudio.Event{Kind: aistudio.EventText, Text: "ok"}
	ch <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "STOP"}
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
	s.buildContentLength = r.ContentLength
	s.buildContentType = r.Header.Get("Content-Type")
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

func (s *recordingStudio) ServeBuildAppEvents(_ context.Context, body []byte, model string, stream bool, accountID string) (<-chan aistudio.Event, error) {
	s.sabCalls++
	s.sabBody = body
	s.sabModel = model
	s.sabStream = stream
	s.sabAccount = accountID
	ch := make(chan aistudio.Event, 2)
	ch <- aistudio.Event{Kind: aistudio.EventText, Text: "ok"}
	ch <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "STOP"}
	close(ch)
	return ch, nil
}

func geminiHandler(studio *recordingStudio) http.Handler {
	s := &server{service: studio, buildApp: studio}
	if studio.catalogOK {
		s.buildCatalog = studio
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1beta/models/{action}", s.handleGeminiAction)
	mux.HandleFunc("GET /v1beta/models", s.handleGeminiModels)
	mux.HandleFunc("GET /v1beta/models/{model}", s.handleGeminiModel)
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

func embeddingsHandler(studio *recordingStudio) http.Handler {
	s := &server{service: studio, buildApp: studio}
	if studio.catalogOK {
		s.buildCatalog = studio
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/embeddings", s.handleOpenAIEmbeddings)
	return mux
}

func catalogFixture() []aistudio.BuildAppModel {
	return []aistudio.BuildAppModel{
		{ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", Methods: []string{"countTokens", "generateContent", "streamGenerateContent"}},
		{ID: "text-embedding-004", DisplayName: "Text Embedding 004", Methods: []string{"batchEmbedContents", "embedContent"}},
	}
}

func TestBuildCatalog_rejectsUnknownModelAndMethod(t *testing.T) {
	tests := []struct {
		name   string
		action string
		body   string
		want   string
		status int
	}{
		{"unknown model", "gemini-9.9-pro:generateContent", `{"contents":[{"parts":[{"text":"x"}]}],"accountID":"acc-build"}`, "model not available for buildapp account", http.StatusBadRequest},
		{"embedding on chat model", "gemini-2.5-flash:embedContent", `{"content":{"parts":[{"text":"x"}]},"accountID":"acc-build"}`, "model not available for buildapp account", http.StatusBadRequest},
		{"generation on embedding model", "text-embedding-004:generateContent", `{"contents":[{"parts":[{"text":"x"}]}],"accountID":"acc-build"}`, "model not available for buildapp account", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			studio := &recordingStudio{mode: aistudio.AccountModeBuildApp, buildModels: catalogFixture(), catalogOK: true}
			rec := postGemini(t, geminiHandler(studio), tt.action, tt.body)
			if rec.Code != tt.status {
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

func TestBuildCatalog_unavailableIs502WithoutWorker(t *testing.T) {
	studio := &recordingStudio{
		mode: aistudio.AccountModeBuildApp, buildModelsErr: aistudio.ErrBuildAppCatalogUnavailable, catalogOK: true,
	}
	rec := postGemini(t, geminiHandler(studio), "gemini-2.5-flash:generateContent",
		`{"contents":[{"parts":[{"text":"x"}]}],"accountID":"acc-build"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "buildapp catalog unavailable") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if studio.buildCalls != 0 {
		t.Fatalf("worker started: %d", studio.buildCalls)
	}
}

func TestBuildCatalog_absentInterfaceKeepsOldBehavior(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp, buildResponse: []byte(`{"ok":true}`)}
	rec := postGemini(t, geminiHandler(studio), "gemini-2.5-flash:generateContent",
		`{"contents":[{"parts":[{"text":"x"}]}],"accountID":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.buildCalls != 1 {
		t.Fatalf("buildCalls=%d", studio.buildCalls)
	}
}

func TestGeminiModels_buildAccountContext_usesBuildCatalog(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp, buildModels: catalogFixture(), catalogOK: true}
	r := httptest.NewRequest(http.MethodGet, "/v1beta/models?account_id=acc-build", nil)
	rec := httptest.NewRecorder()
	geminiHandler(studio).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Models) != 2 || out.Models[0]["name"] != "models/gemini-2.5-flash" {
		t.Fatalf("models = %s", rec.Body.String())
	}
}

func TestGeminiModels_withoutAccountContext_keepsPlaygroundCatalog(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp, buildModels: catalogFixture(), catalogOK: true}
	r := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	rec := httptest.NewRecorder()
	geminiHandler(studio).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// recordingStudio.Models 返回 nil → playground 路径输出空列表；Build 目录未被使用
	if len(out.Models) != 0 || studio.catalogFetches != 0 {
		t.Fatalf("playground leaked build catalog: models=%d fetches=%d", len(out.Models), studio.catalogFetches)
	}
}

func TestHandleOpenAIEmbeddings_unknownModelRejectedByCatalog(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp, buildModels: catalogFixture(), catalogOK: true}
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"missing-embed","input":"x","account_id":"acc-build"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	embeddingsHandler(studio).ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "model not available for buildapp account") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if studio.buildCalls != 0 {
		t.Fatalf("worker started: %d", studio.buildCalls)
	}
}

func TestHandleOpenAIEmbeddings_buildAccount_mapsBatchToOpenAIList(t *testing.T) {
	studio := &recordingStudio{
		mode:          aistudio.AccountModeBuildApp,
		buildResponse: []byte(`{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}]}`),
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-004","input":["a","b"],"account_id":"acc-build"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	embeddingsHandler(studio).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.buildCalls != 1 || studio.buildAccount != "acc-build" {
		t.Fatalf("calls=%d account=%q", studio.buildCalls, studio.buildAccount)
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
	if len(sent.Requests) != 2 || sent.Requests[0]["model"] != "models/text-embedding-004" {
		t.Fatalf("batch body = %s", studio.buildBodies[0])
	}
	var out struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Object    string    `json:"object"`
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" || out.Model != "text-embedding-004" || len(out.Data) != 2 {
		t.Fatalf("openai body = %s", rec.Body.String())
	}
	if out.Data[1].Index != 1 || out.Data[1].Embedding[0] != 0.3 {
		t.Fatalf("data[1] = %#v", out.Data[1])
	}
}

func TestHandleOpenAIEmbeddings_nonBuildAccount_rejectedWithoutWorker(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		query string
		body  string
	}{
		{"playground account", aistudio.AccountModePlayground, "", `{"model":"m","input":"x","account_id":"acc-pg"}`},
		{"missing account", aistudio.AccountModeBuildApp, "", `{"model":"m","input":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			studio := &recordingStudio{mode: tt.mode}
			r := httptest.NewRequest(http.MethodPost, "/v1/embeddings"+tt.query, strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			embeddingsHandler(studio).ServeHTTP(rec, r)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "buildapp account_id") {
				t.Fatalf("body = %s", rec.Body.String())
			}
			if studio.buildCalls != 0 {
				t.Fatalf("worker started: %d", studio.buildCalls)
			}
		})
	}
}

func TestHandleOpenAIEmbeddings_rejectsBadInputWithoutWorker(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"missing model", `{"input":"x","account_id":"acc-build"}`, "model and input are required"},
		{"empty input array", `{"model":"m","input":[],"account_id":"acc-build"}`, "input must not be empty"},
		{"bad input shape", `{"model":"m","input":123,"account_id":"acc-build"}`, "input must be a string or string array"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
			r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			embeddingsHandler(studio).ServeHTTP(rec, r)
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
