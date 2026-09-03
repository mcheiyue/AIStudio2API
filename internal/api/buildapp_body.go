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

const buildAppPlaceholderThoughtSignature = "context_engineering_is_the_way_to_go"

var buildAppBuiltInToolKeys = [...]string{
	"codeExecution",
	"code_execution",
	"googleMaps",
	"google_maps",
	"googleSearch",
	"google_search",
	"googleSearchRetrieval",
	"google_search_retrieval",
	"urlContext",
	"url_context",
}

func (s *server) handleGeminiBuildApp(w http.ResponseWriter, r *http.Request, rawBody []byte, accountID string) {
	body, err := prepareBuildAppGeminiBody(rawBody)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	proxyRequest := r.Clone(r.Context())
	proxyRequest.Body = io.NopCloser(bytes.NewReader(body))
	proxyRequest.ContentLength = int64(len(body))
	proxyRequest.Header.Set("Content-Type", "application/json")
	if err := s.service.ServeBuildApp(r.Context(), w, proxyRequest, accountID); err != nil {
		writeGeminiError(w, http.StatusBadGateway, "buildapp_error", err.Error())
	}
}

// prepareBuildAppGeminiBody keeps a native Gemini request intact for the Build App relay.
// accountID is local routing metadata and must never reach the Google API.
func prepareBuildAppGeminiBody(raw []byte) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body); err != nil {
		return nil, fmt.Errorf("parse native Gemini body: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("native Gemini body must be an object")
	}
	delete(body, "accountID")
	if err := normalizeBuildAppTools(body); err != nil {
		return nil, err
	}
	if err := ensureBuildAppThoughtSignatures(body); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode normalized Gemini body: %w", err)
	}
	return encoded, nil
}

func buildAppBodyFromGenerateRequest(req aistudio.GenerateRequest) ([]byte, error) {
	body := make(map[string]any, 2)
	contents := make([]map[string]any, 0, len(req.Contents))
	for _, content := range req.Contents {
		parts := make([]map[string]any, 0, len(content.Parts))
		for _, part := range content.Parts {
			item := make(map[string]any)
			if part.Text != "" {
				item["text"] = part.Text
			}
			if part.InlineData != nil {
				item["inlineData"] = part.InlineData
			}
			if part.File != nil {
				item["fileData"] = part.File
			}
			if part.FunctionCall != nil {
				args := part.FunctionCall.Arguments
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				var arguments map[string]any
				if err := json.Unmarshal(args, &arguments); err != nil {
					return nil, fmt.Errorf("encode function call arguments: %w", err)
				}
				item["functionCall"] = map[string]any{"name": part.FunctionCall.Name, "args": arguments}
				if part.FunctionCall.ThoughtSignature != "" {
					item["thoughtSignature"] = part.FunctionCall.ThoughtSignature
				}
			}
			if part.FunctionResult != nil {
				var response any
				if err := json.Unmarshal(part.FunctionResult.Content, &response); err != nil {
					return nil, fmt.Errorf("encode function result: %w", err)
				}
				item["functionResponse"] = map[string]any{"name": part.FunctionResult.Name, "response": response}
			}
			if len(item) > 0 {
				parts = append(parts, item)
			}
		}
		contents = append(contents, map[string]any{"role": string(content.Role), "parts": parts})
	}
	body["contents"] = contents
	if req.System != "" {
		body["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": req.System}}}
	}
	if req.Tools.Functions != nil || req.Tools.Google != nil || req.Tools.GoogleSearch != nil {
		tools := make([]map[string]any, 0, 2)
		if len(req.Tools.Functions) > 0 {
			declarations := make([]map[string]any, 0, len(req.Tools.Functions))
			for _, declaration := range req.Tools.Functions {
				item := map[string]any{"name": declaration.Name}
				if declaration.Description != "" {
					item["description"] = declaration.Description
				}
				if len(declaration.Parameters) > 0 {
					parameters, err := normalizeBuildAppSchemaTypes(declaration.Parameters)
					if err != nil {
						return nil, fmt.Errorf("normalize function declaration %q: %w", declaration.Name, err)
					}
					item["parameters"] = json.RawMessage(parameters)
				}
				declarations = append(declarations, item)
			}
			tools = append(tools, map[string]any{"functionDeclarations": declarations})
		}
		if req.Tools.GoogleSearch != nil {
			tools = append(tools, map[string]any{"googleSearch": req.Tools.GoogleSearch})
		}
		body["tools"] = tools
		if req.Tools.ToolConfig.Mode != "" {
			body["toolConfig"] = map[string]any{"functionCallingConfig": map[string]string{"mode": strings.ToUpper(req.Tools.ToolConfig.Mode)}}
		}
	}
	config := make(map[string]any)
	if req.Config.Temperature != nil {
		config["temperature"] = req.Config.Temperature
	}
	if req.Config.TopP != nil {
		config["topP"] = req.Config.TopP
	}
	if req.Config.TopK != nil {
		config["topK"] = req.Config.TopK
	}
	if req.Config.MaxOutputTokens != nil {
		config["maxOutputTokens"] = req.Config.MaxOutputTokens
	}
	if len(req.Config.StopSequences) > 0 {
		config["stopSequences"] = req.Config.StopSequences
	}
	if req.Config.ResponseMIMEType != "" {
		config["responseMimeType"] = req.Config.ResponseMIMEType
	}
	if len(req.Config.ResponseSchema) > 0 {
		config["responseSchema"] = json.RawMessage(req.Config.ResponseSchema)
	}
	if len(config) > 0 {
		body["generationConfig"] = config
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode Build App body: %w", err)
	}
	return prepareBuildAppGeminiBody(encoded)
}

func normalizeBuildAppTools(body map[string]json.RawMessage) error {
	rawTools, ok := body["tools"]
	if !ok {
		return nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return fmt.Errorf("decode Gemini tools: %w", err)
	}
	hasFunctions := false
	hasBuiltIns := false
	for index, rawTool := range tools {
		var tool map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &tool); err != nil || tool == nil {
			continue
		}
		for _, key := range [...]string{"functionDeclarations", "function_declarations"} {
			rawDeclarations, ok := tool[key]
			if !ok {
				continue
			}
			var declarations []json.RawMessage
			if err := json.Unmarshal(rawDeclarations, &declarations); err != nil || len(declarations) == 0 {
				continue
			}
			hasFunctions = true
			for declarationIndex, rawDeclaration := range declarations {
				var declaration map[string]json.RawMessage
				if err := json.Unmarshal(rawDeclaration, &declaration); err != nil || declaration == nil {
					continue
				}
				rawParameters, ok := declaration["parameters"]
				if !ok {
					continue
				}
				parameters, err := normalizeBuildAppSchemaTypes(rawParameters)
				if err != nil {
					return fmt.Errorf("normalize function declaration %d schema: %w", declarationIndex, err)
				}
				declaration["parameters"] = parameters
				encoded, err := json.Marshal(declaration)
				if err != nil {
					return fmt.Errorf("encode function declaration %d: %w", declarationIndex, err)
				}
				declarations[declarationIndex] = encoded
			}
			encoded, err := json.Marshal(declarations)
			if err != nil {
				return fmt.Errorf("encode function declarations: %w", err)
			}
			tool[key] = encoded
		}
		for _, key := range buildAppBuiltInToolKeys {
			if _, ok := tool[key]; ok {
				hasBuiltIns = true
				break
			}
		}
		encoded, err := json.Marshal(tool)
		if err != nil {
			return fmt.Errorf("encode Gemini tool %d: %w", index, err)
		}
		tools[index] = encoded
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return fmt.Errorf("encode Gemini tools: %w", err)
	}
	body["tools"] = encoded
	if hasFunctions && hasBuiltIns {
		return enableBuildAppServerSideToolInvocations(body)
	}
	return nil
}

func normalizeBuildAppSchemaTypes(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err == nil && object != nil {
		for key, value := range object {
			if key == "type" {
				var kind string
				if err := json.Unmarshal(value, &kind); err == nil {
					normalized, err := json.Marshal(strings.ToUpper(kind))
					if err != nil {
						return nil, fmt.Errorf("encode schema type: %w", err)
					}
					object[key] = normalized
					continue
				}
			}
			normalized, err := normalizeBuildAppSchemaTypes(value)
			if err != nil {
				return nil, err
			}
			object[key] = normalized
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("encode schema object: %w", err)
		}
		return encoded, nil
	}
	var array []json.RawMessage
	if err := json.Unmarshal(raw, &array); err == nil && array != nil {
		for index, value := range array {
			normalized, err := normalizeBuildAppSchemaTypes(value)
			if err != nil {
				return nil, err
			}
			array[index] = normalized
		}
		encoded, err := json.Marshal(array)
		if err != nil {
			return nil, fmt.Errorf("encode schema array: %w", err)
		}
		return encoded, nil
	}
	return raw, nil
}

func enableBuildAppServerSideToolInvocations(body map[string]json.RawMessage) error {
	config := make(map[string]json.RawMessage)
	if rawConfig, ok := body["toolConfig"]; ok {
		if err := json.Unmarshal(rawConfig, &config); err != nil || config == nil {
			config = make(map[string]json.RawMessage)
		}
	}
	if enabled, ok := config["includeServerSideToolInvocations"]; ok && string(enabled) == "true" {
		return nil
	}
	config["includeServerSideToolInvocations"] = json.RawMessage("true")
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode Gemini tool config: %w", err)
	}
	body["toolConfig"] = encoded
	return nil
}

func ensureBuildAppThoughtSignatures(body map[string]json.RawMessage) error {
	rawContents, ok := body["contents"]
	if !ok {
		return nil
	}
	var contents []json.RawMessage
	if err := json.Unmarshal(rawContents, &contents); err != nil {
		return fmt.Errorf("decode Gemini contents: %w", err)
	}
	for contentIndex, rawContent := range contents {
		var content map[string]json.RawMessage
		if err := json.Unmarshal(rawContent, &content); err != nil || content == nil {
			continue
		}
		rawParts, ok := content["parts"]
		if !ok {
			continue
		}
		var parts []json.RawMessage
		if err := json.Unmarshal(rawParts, &parts); err != nil {
			return fmt.Errorf("decode content %d parts: %w", contentIndex, err)
		}
		signatureAdded := false
		for partIndex, rawPart := range parts {
			var part map[string]json.RawMessage
			if err := json.Unmarshal(rawPart, &part); err != nil || part == nil {
				continue
			}
			functionCall, hasFunctionCall := part["functionCall"]
			if !hasFunctionCall || string(functionCall) == "null" || signatureAdded || buildAppHasThoughtSignature(part) {
				continue
			}
			part["thoughtSignature"] = json.RawMessage(`"context_engineering_is_the_way_to_go"`)
			signatureAdded = true
			encoded, err := json.Marshal(part)
			if err != nil {
				return fmt.Errorf("encode content %d part %d: %w", contentIndex, partIndex, err)
			}
			parts[partIndex] = encoded
		}
		encodedParts, err := json.Marshal(parts)
		if err != nil {
			return fmt.Errorf("encode content %d parts: %w", contentIndex, err)
		}
		content["parts"] = encodedParts
		encodedContent, err := json.Marshal(content)
		if err != nil {
			return fmt.Errorf("encode content %d: %w", contentIndex, err)
		}
		contents[contentIndex] = encodedContent
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		return fmt.Errorf("encode Gemini contents: %w", err)
	}
	body["contents"] = encoded
	return nil
}

func buildAppHasThoughtSignature(part map[string]json.RawMessage) bool {
	raw, ok := part["thoughtSignature"]
	if !ok {
		return false
	}
	return string(raw) != `""` && string(raw) != "null"
}
