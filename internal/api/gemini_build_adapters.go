package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

type captureWriter struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header {
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}

func (c *captureWriter) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.buf.Write(p)
}

func (c *captureWriter) WriteHeader(status int) {
	c.code = status
}

func (s *server) handleGeminiBuildNative(w http.ResponseWriter, r *http.Request, rawBody []byte, model, method, accountID string) {
	if err := s.checkBuildAppCatalog(r.Context(), accountID, model, method); err != nil {
		writeBuildAppCatalogError(w, err, writeGeminiError)
		return
	}
	body, path, splitEmbed, err := prepareGeminiBuildNative(rawBody, model, method)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	proxy := r.Clone(r.Context())
	proxy.URL.Path = path
	proxy.Body = io.NopCloser(bytes.NewReader(body))
	proxy.ContentLength = int64(len(body))
	proxy.Header.Set("Content-Type", "application/json")

	rec := &captureWriter{header: make(http.Header)}
	if err := s.service.ServeBuildApp(r.Context(), rec, proxy, accountID); err != nil {
		writeGeminiError(w, http.StatusBadGateway, "buildapp_error", err.Error())
		return
	}
	out := rec.buf.Bytes()
	if splitEmbed && rec.code < 400 {
		converted, convErr := aistudio.ConvertBatchEmbedResponseToEmbedContent(out)
		if convErr != nil {
			writeGeminiError(w, http.StatusBadGateway, "buildapp_error", convErr.Error())
			return
		}
		out = converted
	}
	writeCapturedResponse(w, rec, out)
}

func writeCapturedResponse(w http.ResponseWriter, rec *captureWriter, body []byte) {
	for k, vs := range rec.header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	code := rec.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// checkBuildAppCatalog 用 Build 独立目录校验 model+method。
// catalog 未装配（单元 stub）→ 放行；目录拉取失败 → ErrBuildAppCatalogUnavailable；
// 模型不在目录或不支持该方法 → ErrBuildAppModelNotAvailable。
func (s *server) checkBuildAppCatalog(ctx context.Context, accountID, model, method string) error {
	if s.buildCatalog == nil {
		return nil
	}
	models, err := s.buildCatalog.BuildAppModels(ctx, accountID)
	if err != nil {
		return err
	}
	return aistudio.CheckBuildAppMethod(models, model, method)
}

// writeBuildAppCatalogError 把目录校验错误映射为对应风格的 HTTP 错误：
// 目录不可用 → 502，模型/方法不允许 → 400。
func writeBuildAppCatalogError(w http.ResponseWriter, err error, write func(http.ResponseWriter, int, string, string)) {
	if errors.Is(err, aistudio.ErrBuildAppCatalogUnavailable) {
		write(w, http.StatusBadGateway, "buildapp_error", err.Error())
		return
	}
	write(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
}

// buildAppCatalogForRequest 解析 models 端点的 buildapp 账号上下文（?account_id=）。
// 返回 (目录, 是否 buildapp 上下文, 目录错误)；非 buildapp 上下文时三个值全零，调用方走 Playground 原路径。
func (s *server) buildAppCatalogForRequest(r *http.Request) ([]aistudio.BuildAppModel, bool, error) {
	if s.buildCatalog == nil {
		return nil, false, nil
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
	if accountID == "" || s.service.AccountMode(accountID) != aistudio.AccountModeBuildApp {
		return nil, false, nil
	}
	models, err := s.buildCatalog.BuildAppModels(r.Context(), accountID)
	if err != nil {
		return nil, true, err
	}
	return models, true, nil
}

func openAIBuildModelObject(m aistudio.BuildAppModel) map[string]any {
	return map[string]any{
		"id":                           m.ID,
		"object":                       "model",
		"created":                      0,
		"owned_by":                     "google",
		"name":                         m.DisplayName,
		"description":                  m.Description,
		"supported_generation_methods": m.Methods,
		"input_token_limit":            m.InputTokenLimit,
		"output_token_limit":           m.OutputTokenLimit,
	}
}

func geminiBuildModelObject(m aistudio.BuildAppModel) map[string]any {
	return map[string]any{
		"name":                       "models/" + m.ID,
		"displayName":                m.DisplayName,
		"description":                m.Description,
		"supportedGenerationMethods": m.Methods,
		"inputTokenLimit":            m.InputTokenLimit,
		"outputTokenLimit":           m.OutputTokenLimit,
	}
}

func prepareGeminiBuildNative(raw []byte, model, method string) ([]byte, string, bool, error) {
	model = strings.TrimPrefix(strings.TrimSpace(model), "models/")
	if model == "" {
		return nil, "", false, fmt.Errorf("model is required")
	}
	stripped, err := stripBuildAppAccountID(raw)
	if err != nil {
		return nil, "", false, err
	}
	pathPrefix := "/v1beta/models/" + model + ":"
	switch method {
	case "countTokens":
		if err := requireJSONArray(stripped, "contents"); err != nil {
			return nil, "", false, err
		}
		return stripped, pathPrefix + "countTokens", false, nil
	case "embedContent":
		body, err := aistudio.ConvertEmbedContentBodyToBatch(stripped, model)
		if err != nil {
			return nil, "", false, err
		}
		return body, pathPrefix + "batchEmbedContents", true, nil
	case "batchEmbedContents":
		if err := requireJSONArray(stripped, "requests"); err != nil {
			return nil, "", false, err
		}
		return stripped, pathPrefix + "batchEmbedContents", false, nil
	default:
		return nil, "", false, fmt.Errorf("unknown method: %s", method)
	}
}

func stripBuildAppAccountID(raw []byte) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parse native Gemini body: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("native Gemini body must be an object")
	}
	delete(body, "accountID")
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode native Gemini body: %w", err)
	}
	return encoded, nil
}

func requireJSONArray(raw []byte, field string) error {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	item, ok := body[field]
	if !ok || len(item) == 0 || string(item) == "null" {
		return fmt.Errorf("%s is required", field)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(item, &arr); err != nil {
		return fmt.Errorf("%s is required", field)
	}
	if len(arr) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

// handleOpenAIEmbeddings 服务 POST /v1/embeddings：仅 buildapp 账号可用。
// OpenAI input 转 native batchEmbedContents（每条 input 一个 request），响应映射回 OpenAI list 形状。
func (s *server) handleOpenAIEmbeddings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model     string          `json:"model"`
		Input     json.RawMessage `json:"input"`
		AccountID string          `json:"account_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "parse embeddings body: "+err.Error())
		return
	}
	model := strings.TrimPrefix(strings.TrimSpace(request.Model), "models/")
	if model == "" || len(request.Input) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "model and input are required")
		return
	}
	inputs, err := decodeEmbeddingInputs(request.Input)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if request.AccountID == "" {
		request.AccountID = strings.TrimSpace(r.URL.Query().Get("account_id"))
	}
	if request.AccountID == "" || s.service.AccountMode(request.AccountID) != aistudio.AccountModeBuildApp {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "embeddings require a buildapp account_id")
		return
	}
	if err := s.checkBuildAppCatalog(r.Context(), request.AccountID, model, "embedContent"); err != nil {
		writeBuildAppCatalogError(w, err, writeOpenAIError)
		return
	}
	body, err := buildOpenAIBatchEmbedBody(model, inputs)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	proxy := r.Clone(r.Context())
	proxy.URL.Path = "/v1beta/models/" + model + ":batchEmbedContents"
	proxy.Body = io.NopCloser(bytes.NewReader(body))
	proxy.ContentLength = int64(len(body))
	proxy.Header.Set("Content-Type", "application/json")

	rec := &captureWriter{header: make(http.Header)}
	if err := s.service.ServeBuildApp(r.Context(), rec, proxy, request.AccountID); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "buildapp_error", err.Error())
		return
	}
	if rec.code >= 400 {
		writeOpenAIError(w, http.StatusBadGateway, "buildapp_error", "upstream embeddings failed with status "+strconv.Itoa(rec.code))
		return
	}
	data, err := mapBatchEmbedToOpenAI(rec.buf.Bytes(), request.Model)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "buildapp_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// decodeEmbeddingInputs 接受 string 或 string 数组两种 OpenAI input 形状。
func decodeEmbeddingInputs(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("input must be a string or string array")
	}
	if len(many) == 0 {
		return nil, fmt.Errorf("input must not be empty")
	}
	for _, item := range many {
		if strings.TrimSpace(item) == "" {
			return nil, fmt.Errorf("input must not contain empty entries")
		}
	}
	return many, nil
}

// buildOpenAIBatchEmbedBody 构造 native batchEmbedContents 请求体。
func buildOpenAIBatchEmbedBody(model string, inputs []string) ([]byte, error) {
	requests := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		requests = append(requests, map[string]any{
			"model":   "models/" + model,
			"content": map[string]any{"parts": []map[string]any{{"text": input}}},
		})
	}
	encoded, err := json.Marshal(map[string]any{"requests": requests})
	if err != nil {
		return nil, fmt.Errorf("encode batchEmbedContents body: %w", err)
	}
	return encoded, nil
}

// mapBatchEmbedToOpenAI 把 native batchEmbedContents 响应映射为 OpenAI embeddings 形状。
func mapBatchEmbedToOpenAI(raw []byte, model string) (map[string]any, error) {
	var batch struct {
		Embeddings []json.RawMessage `json:"embeddings"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		return nil, fmt.Errorf("decode batchEmbedContents response: %w", err)
	}
	if len(batch.Embeddings) == 0 {
		return nil, fmt.Errorf("batchEmbedContents response did not contain embeddings")
	}
	data := make([]map[string]any, 0, len(batch.Embeddings))
	for index, item := range batch.Embeddings {
		var values struct {
			Values []float64 `json:"values"`
		}
		if err := json.Unmarshal(item, &values); err != nil {
			return nil, fmt.Errorf("decode embedding[%d]: %w", index, err)
		}
		data = append(data, map[string]any{
			"object":    "embedding",
			"embedding": values.Values,
			"index":     index,
		})
	}
	return map[string]any{"object": "list", "data": data, "model": model}, nil
}
