package aistudio

import (
	"encoding/json"
	"testing"
)

func TestParseBuildAppJSON_TextResponse(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{"text": "PROBE_OK"}],
				"role": "model"
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 9,
			"candidatesTokenCount": 4,
			"totalTokenCount": 34
		}
	}`)
	events, err := parseBuildAppJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (text + finish), got %d", len(events))
	}
	if events[0].Kind != EventText || events[0].Text != "PROBE_OK" {
		t.Errorf("expected EventText 'PROBE_OK', got kind=%v text=%q", events[0].Kind, events[0].Text)
	}
	// find finish
	found := false
	for _, e := range events {
		if e.Kind == EventFinish && e.FinishReason == "STOP" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected EventFinish STOP")
	}
}

func TestParseBuildAppJSON_FunctionCall(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {
				"parts": [{
					"functionCall": {"name": "get_weather", "args": {"city": "Beijing"}},
					"text": ""
				}],
				"role": "model"
			},
			"finishReason": "STOP"
		}]
	}`)
	events, err := parseBuildAppJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == EventToolCall && e.ToolCall != nil && e.ToolCall.Name == "get_weather" {
			found = true
			// verify args round-trip
			var args map[string]any
			if err := json.Unmarshal(e.ToolCall.Arguments, &args); err != nil {
				t.Fatalf("invalid tool call args: %v", err)
			}
			if args["city"] != "Beijing" {
				t.Errorf("expected city=Beijing, got %v", args["city"])
			}
			break
		}
	}
	if !found {
		t.Error("expected EventToolCall get_weather")
	}
}

func TestParseBuildAppJSON_Error(t *testing.T) {
	raw := []byte(`{
		"error": {
			"code": 403,
			"status": "PERMISSION_DENIED",
			"message": "caller does not have permission"
		}
	}`)
	events, err := parseBuildAppJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != EventError {
		t.Fatalf("expected 1 EventError, got %d events kind=%v", len(events), events[0].Kind)
	}
}

func TestBuildAppResponseEvents_Stream(t *testing.T) {
	// simulate two SSE chunks
	data := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}
data: [DONE]
`)
	events, err := buildAppResponseEvents(data, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	if events[0].Kind != EventText || events[0].Text != "Hello" {
		t.Errorf("expected text 'Hello', got kind=%v text=%q", events[0].Kind, events[0].Text)
	}
}

func TestBuildAppResponseEvents_NonStream(t *testing.T) {
	data := []byte(`{"candidates":[{"content":{"parts":[{"text":"weather is sunny"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"totalTokenCount":8}}`)
	events, err := buildAppResponseEvents(data, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == EventText && e.Text == "weather is sunny" {
			found = true
		}
	}
	if !found {
		t.Error("expected text 'weather is sunny'")
	}
}
