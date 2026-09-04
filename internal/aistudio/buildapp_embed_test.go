package aistudio

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConvertEmbedContentBodyToBatch_setsModelsPrefix(t *testing.T) {
	raw := []byte(`{"content":{"parts":[{"text":"hi"}]},"taskType":"RETRIEVAL_DOCUMENT","accountID":"acc"}`)
	got, err := ConvertEmbedContentBodyToBatch(raw, "text-embedding-004")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Requests) != 1 {
		t.Fatalf("requests len = %d", len(body.Requests))
	}
	if body.Requests[0]["model"] != "models/text-embedding-004" {
		t.Fatalf("model = %v", body.Requests[0]["model"])
	}
	if _, ok := body.Requests[0]["accountID"]; ok {
		t.Fatal("accountID leaked into batch request")
	}
	content, _ := body.Requests[0]["content"].(map[string]any)
	if content == nil {
		t.Fatal("missing content")
	}
}

func TestConvertEmbedContentBodyToBatch_rejectsMissingContent(t *testing.T) {
	_, err := ConvertEmbedContentBodyToBatch([]byte(`{"taskType":"RETRIEVAL_DOCUMENT"}`), "text-embedding-004")
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestConvertBatchEmbedResponseToEmbedContent_takesFirstEmbedding(t *testing.T) {
	raw := []byte(`{"embeddings":[{"values":[0.1,0.2]},{"values":[9]}],"usageMetadata":{"totalTokenCount":3}}`)
	got, err := ConvertBatchEmbedResponseToEmbedContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		UsageMetadata struct {
			TotalTokenCount int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Embedding.Values) != 2 || out.Embedding.Values[0] != 0.1 {
		t.Fatalf("embedding = %#v", out.Embedding)
	}
	if out.UsageMetadata.TotalTokenCount != 3 {
		t.Fatalf("usage = %#v", out.UsageMetadata)
	}
	if strings.Contains(string(got), `"embeddings"`) {
		t.Fatalf("batch field leaked: %s", got)
	}
}

func TestConvertBatchEmbedResponseToEmbedContent_rejectsEmpty(t *testing.T) {
	_, err := ConvertBatchEmbedResponseToEmbedContent([]byte(`{"embeddings":[]}`))
	if err == nil {
		t.Fatal("expected error")
	}
}
