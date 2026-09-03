package api

import (
	"encoding/json"
	"testing"
)

func TestPrepareBuildAppGeminiBody_preserves_native_tool_request_when_build_account_selected(t *testing.T) {
	// Given
	raw := []byte(`{
		"accountID":"build@example.com",
		"contents":[{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}}]}],
		"tools":[
			{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]},
			{"googleSearch":{}}
		],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}},
		"generationConfig":{"maxOutputTokens":20}
	}`)

	// When
	got, err := prepareBuildAppGeminiBody(raw)

	// Then
	if err != nil {
		t.Fatalf("prepareBuildAppGeminiBody() error = %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(got, &top); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if _, ok := top["accountID"]; ok {
		t.Fatal("accountID must not reach Google")
	}

	var body struct {
		Contents []struct {
			Parts []struct {
				ThoughtSignature string `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []struct {
				Parameters struct {
					Type       string `json:"type"`
					Properties struct {
						City struct {
							Type string `json:"type"`
						} `json:"city"`
					} `json:"properties"`
				} `json:"parameters"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode                 string   `json:"mode"`
				AllowedFunctionNames []string `json:"allowedFunctionNames"`
			} `json:"functionCallingConfig"`
			IncludeServerSideToolInvocations bool `json:"includeServerSideToolInvocations"`
		} `json:"toolConfig"`
		GenerationConfig struct {
			MaxOutputTokens int `json:"maxOutputTokens"`
		} `json:"generationConfig"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatalf("decode normalized body: %v", err)
	}
	if body.GenerationConfig.MaxOutputTokens != 20 {
		t.Fatalf("generationConfig was not preserved: %+v", body.GenerationConfig)
	}
	if body.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Fatalf("tool mode = %q, want ANY", body.ToolConfig.FunctionCallingConfig.Mode)
	}
	if len(body.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) != 1 || body.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != "get_weather" {
		t.Fatalf("allowed function names were not preserved: %#v", body.ToolConfig.FunctionCallingConfig.AllowedFunctionNames)
	}
	if !body.ToolConfig.IncludeServerSideToolInvocations {
		t.Fatal("built-in and function tools must enable server-side invocations")
	}
	if len(body.Tools) != 2 || len(body.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("function declarations were not preserved: %+v", body.Tools)
	}
	parameters := body.Tools[0].FunctionDeclarations[0].Parameters
	if parameters.Type != "OBJECT" || parameters.Properties.City.Type != "STRING" {
		t.Fatalf("schema types were not normalized: %+v", parameters)
	}
	if body.Contents[0].Parts[0].ThoughtSignature != buildAppPlaceholderThoughtSignature {
		t.Fatalf("functionCall thoughtSignature = %q, want placeholder", body.Contents[0].Parts[0].ThoughtSignature)
	}
}
