package aistudio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type buildAppGeminiResponse struct {
	Candidates []struct {
		Content *struct {
			Parts []struct {
				Text             string `json:"text"`
				ThoughtSignature string `json:"thoughtSignature"`
				FunctionCall     *struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		TotalTokenCount      int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func buildAppResponseEvents(data []byte, stream bool) ([]Event, error) {
	if !stream {
		return parseBuildAppJSON(data)
	}
	var events []Event
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "data:"))
		if line == "" || line == "[DONE]" || line == ":" || strings.HasPrefix(line, "event:") {
			continue
		}
		parsed, err := parseBuildAppJSON([]byte(line))
		if err != nil {
			return nil, err
		}
		events = append(events, parsed...)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Build App stream: %w", err)
	}
	return events, nil
}

func parseBuildAppJSON(data []byte) ([]Event, error) {
	var response buildAppGeminiResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Build App response: %w", err)
	}
	if response.Error != nil {
		return []Event{{Kind: EventError, Err: fmt.Errorf("Build App upstream %d %s: %s", response.Error.Code, response.Error.Status, response.Error.Message)}}, nil
	}
	events := make([]Event, 0)
	for _, candidate := range response.Candidates {
		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					events = append(events, Event{Kind: EventText, Text: part.Text})
				}
				if part.FunctionCall != nil {
					args, err := json.Marshal(part.FunctionCall.Args)
					if err != nil {
						return nil, fmt.Errorf("encode Build App function arguments: %w", err)
					}
					events = append(events, Event{Kind: EventToolCall, ToolCall: &FunctionCall{Name: part.FunctionCall.Name, Arguments: args, ThoughtSignature: part.ThoughtSignature}, ThoughtSignature: part.ThoughtSignature})
				}
			}
		}
		if candidate.FinishReason != "" {
			events = append(events, Event{Kind: EventFinish, FinishReason: candidate.FinishReason})
		}
	}
	if response.UsageMetadata != nil {
		u := response.UsageMetadata
		events = append(events, Event{Kind: EventUsage, Usage: &Usage{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount, TotalTokens: u.TotalTokenCount}})
	}
	return events, nil
}

func (w *BuildAppWorker) serveEvents(ctx context.Context, r *http.Request, body []byte, stream bool) (<-chan Event, error) {
	reqID, messages, err := w.transport.SubmitRequest(r, body)
	if err != nil {
		return nil, err
	}
	events := make(chan Event)
	clickCtx, stopClick := context.WithCancel(ctx)
	go func() {
		defer stopClick()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if w.session != nil {
				if clicked, clickErr := w.session.ClickLaunch(); clickErr == nil && clicked {
					return
				}
			}
			select {
			case <-clickCtx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	go func() {
		defer close(events)
		defer w.server.Done(reqID)
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				if message.EventType == "response_headers" && message.Status >= 400 {
					events <- Event{Kind: EventError, Err: fmt.Errorf("Build App upstream returned HTTP %d", message.Status)}
					return
				}
				if message.EventType == "chunk" {
					parsed, parseErr := buildAppResponseEvents([]byte(message.Data), stream)
					if parseErr != nil {
						events <- Event{Kind: EventError, Err: parseErr}
						return
					}
					for _, event := range parsed {
						events <- event
					}
				}
				if message.EventType == "error" {
					events <- Event{Kind: EventError, Err: fmt.Errorf("Build App relay: %s", message.Message)}
					return
				}
				if message.EventType == "stream_close" {
					return
				}
			}
		}
	}()
	return events, nil
}
