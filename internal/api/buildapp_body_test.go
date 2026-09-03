package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

func TestBuildAppBodyFromGenerateRequest_BasicText(t *testing.T) {
	req := aistudio.GenerateRequest{
		System: "You are a helpful assistant.",
		Contents: []aistudio.Content{
			{Role: "user", Parts: []aistudio.Part{{Text: "Hello"}}},
		},
	}
	body, err := buildAppBodyFromGenerateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// systemInstruction must use camelCase
	if _, ok := m["systemInstruction"]; !ok {
		t.Error("missing systemInstruction")
	}
	// contents must be present
	if _, ok := m["contents"]; !ok {
		t.Error("missing contents")
	}
}

func TestBuildAppBodyFromGenerateRequest_Tools(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	req := aistudio.GenerateRequest{
		Model: "gemini-2.5-flash",
		Contents: []aistudio.Content{
			{Role: "user", Parts: []aistudio.Part{{Text: "Weather?"}}},
		},
		Tools: aistudio.Tools{
			Functions: []aistudio.FunctionDeclaration{
				{Name: "get_weather", Description: "Get weather", Parameters: params},
			},
			ToolConfig: aistudio.ToolConfig{Mode: "auto"},
		},
	}
	body, err := buildAppBodyFromGenerateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := m["tools"]; !ok {
		t.Error("missing tools")
	}
	// toolConfig must have functionCallingConfig
	tc, ok := m["toolConfig"]
	if !ok {
		t.Error("missing toolConfig")
	} else {
		var tcMap map[string]any
		if err := json.Unmarshal(tc, &tcMap); err != nil {
			t.Fatal(err)
		}
		fcc, ok := tcMap["functionCallingConfig"]
		if !ok {
			t.Error("missing functionCallingConfig in toolConfig")
		} else {
			fccMap, ok := fcc.(map[string]any)
			if !ok {
				t.Error("functionCallingConfig is not a map")
			} else if fccMap["mode"] != "AUTO" {
				t.Errorf("expected AUTO mode, got %v", fccMap["mode"])
			}
		}
	}
}

func TestBuildAppBodyFromGenerateRequest_SchemaTypesUppercased(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	req := aistudio.GenerateRequest{
		Model: "gemini-2.5-flash",
		Contents: []aistudio.Content{
			{Role: "user", Parts: []aistudio.Part{{Text: "Call"}}},
		},
		Tools: aistudio.Tools{
			Functions: []aistudio.FunctionDeclaration{
				{Name: "fn", Parameters: params},
			},
		},
	}
	body, err := buildAppBodyFromGenerateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	// parse back to verify schema type is uppercase
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	tools := m["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	decls := tool0["functionDeclarations"].([]any)
	decl0 := decls[0].(map[string]any)
	parameters := decl0["parameters"].(map[string]any)
	if parameters["type"] != "OBJECT" {
		t.Errorf("expected OBJECT type, got %v", parameters["type"])
	}
	props := parameters["properties"].(map[string]any)
	name := props["name"].(map[string]any)
	if name["type"] != "STRING" {
		t.Errorf("expected STRING type, got %v", name["type"])
	}
}

func TestBuildAppBodyFromGenerateRequest_GenerationConfig(t *testing.T) {
	temp := 0.7
	maxTokens := int64(1024)
	req := aistudio.GenerateRequest{
		Model: "gemini-2.5-flash",
		Contents: []aistudio.Content{
			{Role: "user", Parts: []aistudio.Part{{Text: "Hello"}}},
		},
		Config: aistudio.GenerationConfig{
			Temperature:     &temp,
			MaxOutputTokens: &maxTokens,
		},
	}
	body, err := buildAppBodyFromGenerateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	gc, ok := m["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("missing generationConfig")
	}
	if gc["temperature"] != 0.7 {
		t.Errorf("expected temperature 0.7, got %v", gc["temperature"])
	}
	if gc["maxOutputTokens"] != float64(1024) {
		t.Errorf("expected maxOutputTokens 1024, got %v", gc["maxOutputTokens"])
	}
}

func TestBuildAppBodyFromGenerateRequest_AccountIDStripped(t *testing.T) {
	req := aistudio.GenerateRequest{
		AccountID: "aa2267610721@gmail.com",
		Model:     "gemini-2.5-flash",
		Contents:  []aistudio.Content{{Role: "user", Parts: []aistudio.Part{{Text: "test"}}}},
	}
	body, err := buildAppBodyFromGenerateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	// account_id must NOT appear in output
	if strings.Contains(string(body), "account_id") || strings.Contains(string(body), "accountID") {
		t.Error("account_id leaked into native Gemini body")
	}
}

func TestBuildAppBodyFromGenerateRequest_FunctionCall(t *testing.T) {
	args := json.RawMessage(`{"city":"Beijing"}`)
	req := aistudio.GenerateRequest{
		Model: "gemini-2.5-flash",
		Contents: []aistudio.Content{
			{Role: "model", Parts: []aistudio.Part{
				{FunctionCall: &aistudio.FunctionCall{Name: "get_weather", Arguments: args}},
			}},
			{Role: "user", Parts: []aistudio.Part{
				{FunctionResult: &aistudio.FunctionResult{
					Name:    "get_weather",
					Content: json.RawMessage(`{"temp":"22C"}`),
				}},
			}},
		},
	}
	body, err := buildAppBodyFromGenerateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	contents := m["contents"].([]any)
	// model content should have functionCall
	modelContent := contents[0].(map[string]any)
	modelParts := modelContent["parts"].([]any)
	modelPart := modelParts[0].(map[string]any)
	if _, ok := modelPart["functionCall"]; !ok {
		t.Error("missing functionCall in model content")
	}
	// user content should have functionResponse
	userContent := contents[1].(map[string]any)
	userParts := userContent["parts"].([]any)
	userPart := userParts[0].(map[string]any)
	if _, ok := userPart["functionResponse"]; !ok {
		t.Error("missing functionResponse in user content")
	}
}
