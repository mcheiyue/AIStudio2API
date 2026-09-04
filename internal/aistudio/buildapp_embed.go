package aistudio

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertEmbedContentBodyToBatch 把 native embedContent 请求转成 batchEmbedContents。
// 与 iBUHub _convertEmbedContentBodyToBatch 对齐：requests[0].model = models/<model>。
func ConvertEmbedContentBodyToBatch(raw []byte, modelName string) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parse embedContent body: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("embedContent body must be an object")
	}
	delete(body, "accountID")
	if _, ok := body["content"]; !ok {
		return nil, fmt.Errorf("content is required")
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return nil, fmt.Errorf("model is required")
	}
	body["model"] = "models/" + modelName
	encoded, err := json.Marshal(map[string]any{"requests": []any{body}})
	if err != nil {
		return nil, fmt.Errorf("encode batchEmbedContents body: %w", err)
	}
	return encoded, nil
}

// ConvertBatchEmbedResponseToEmbedContent 把 batch 响应拆成单条 embedContent。
// 与 iBUHub _convertBatchEmbedResponseToEmbedContent 对齐。
func ConvertBatchEmbedResponseToEmbedContent(raw []byte) ([]byte, error) {
	var batch struct {
		Embeddings    []json.RawMessage `json:"embeddings"`
		UsageMetadata json.RawMessage   `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		return nil, fmt.Errorf("decode batchEmbedContents response: %w", err)
	}
	if len(batch.Embeddings) == 0 {
		return nil, fmt.Errorf("batchEmbedContents response did not contain embeddings[0]")
	}
	out := struct {
		Embedding     json.RawMessage `json:"embedding"`
		UsageMetadata json.RawMessage `json:"usageMetadata,omitempty"`
	}{
		Embedding:     batch.Embeddings[0],
		UsageMetadata: batch.UsageMetadata,
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode embedContent response: %w", err)
	}
	return encoded, nil
}
