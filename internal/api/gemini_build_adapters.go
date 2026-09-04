package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	for k, vs := range rec.header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(out)))
	code := rec.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(out)
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
