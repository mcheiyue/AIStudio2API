package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// 兼容端点回归：OpenAI Chat / Responses / Anthropic 的 buildapp 分支与 Playground 隔离。

func compatHandler(studio *recordingStudio, route string, handler func(*server, http.ResponseWriter, *http.Request)) http.Handler {
	s := &server{service: studio, buildApp: studio, responseStates: newResponseStateStore()}
	if studio.catalogOK {
		s.buildCatalog = studio
	}
	mux := http.NewServeMux()
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { handler(s, w, r) })
	return mux
}

func postJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestChatCompletions_buildAccount_routesThroughBuildEvents(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
	rec := postJSON(compatHandler(studio, "POST /v1/chat/completions", (*server).handleChatCompletions), "/v1/chat/completions",
		`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hi"}],"account_id":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.sabCalls != 1 || studio.generateCalls != 0 {
		t.Fatalf("sabCalls=%d generateCalls=%d", studio.sabCalls, studio.generateCalls)
	}
	if studio.sabAccount != "acc-build" || studio.sabModel != "gemini-3.7-flash" || studio.sabStream {
		t.Fatalf("sab args account=%q model=%q stream=%v", studio.sabAccount, studio.sabModel, studio.sabStream)
	}
	if !strings.Contains(string(studio.sabBody), `"text":"hi"`) {
		t.Fatalf("build body lost message: %s", studio.sabBody)
	}
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := decodeResponseBody(rec, &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "chat.completion" || len(out.Choices) != 1 || out.Choices[0].Message.Role != "assistant" {
		t.Fatalf("chat completion shape = %s", rec.Body.String())
	}
}

func TestChatCompletions_withoutAccount_keepsPlaygroundPath(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
	rec := postJSON(compatHandler(studio, "POST /v1/chat/completions", (*server).handleChatCompletions), "/v1/chat/completions",
		`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.sabCalls != 0 || studio.generateCalls != 1 {
		t.Fatalf("sabCalls=%d generateCalls=%d", studio.sabCalls, studio.generateCalls)
	}
}

func TestResponses_buildAccount_routesThroughBuildEvents(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
	rec := postJSON(compatHandler(studio, "POST /v1/responses", (*server).handleResponses), "/v1/responses",
		`{"model":"gemini-3.7-flash","input":"hi","account_id":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.sabCalls != 1 || studio.generateCalls != 0 {
		t.Fatalf("sabCalls=%d generateCalls=%d", studio.sabCalls, studio.generateCalls)
	}
	if !strings.Contains(string(studio.sabBody), `"text":"hi"`) {
		t.Fatalf("build body lost input: %s", studio.sabBody)
	}
	var out struct {
		ID     string `json:"id"`
		Object string `json:"object"`
	}
	if err := decodeResponseBody(rec, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.ID, "resp_") {
		t.Fatalf("response id = %q", out.ID)
	}
}

func TestAnthropicMessages_buildAccount_routesThroughBuildEvents(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
	rec := postJSON(compatHandler(studio, "POST /v1/messages", (*server).handleAnthropicMessages), "/v1/messages",
		`{"model":"gemini-3.7-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"account_id":"acc-build"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if studio.sabCalls != 1 || studio.generateCalls != 0 {
		t.Fatalf("sabCalls=%d generateCalls=%d", studio.sabCalls, studio.generateCalls)
	}
	if !strings.Contains(string(studio.sabBody), `"text":"hi"`) {
		t.Fatalf("build body lost message: %s", studio.sabBody)
	}
	var out struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if err := decodeResponseBody(rec, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.ID, "msg_") || out.Type != "message" || out.Role != "assistant" || len(out.Content) == 0 {
		t.Fatalf("anthropic shape = %s", rec.Body.String())
	}
}

// Live/Robotics 是独立 Bidi 协议，任何情况下不得进入 Build 中继。
func TestLiveAndRobotics_neverTouchBuild(t *testing.T) {
	studio := &recordingStudio{mode: aistudio.AccountModeBuildApp}
	s := &server{service: studio, buildApp: studio}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/live", s.handleGeminiLive)
	mux.HandleFunc("GET /v1/robotics/stream", s.handleRoboticsStream)
	for _, path := range []string{"/v1/live", "/v1/robotics/stream"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		if studio.sabCalls != 0 || studio.buildCalls != 0 {
			t.Fatalf("%s touched build: sabCalls=%d buildCalls=%d", path, studio.sabCalls, studio.buildCalls)
		}
	}
}

func decodeResponseBody(rec *httptest.ResponseRecorder, out any) error {
	return json.Unmarshal(rec.Body.Bytes(), out)
}
