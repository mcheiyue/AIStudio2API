package aistudio

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const listModelsFixture = `{
  "models": [
    {
      "name": "models/gemini-2.5-flash",
      "displayName": "Gemini 2.5 Flash",
      "description": "fast",
      "inputTokenLimit": 1048576,
      "outputTokenLimit": 8192,
      "supportedGenerationMethods": ["generateContent", "countTokens", "bidiGenerateContent"]
    },
    {
      "name": "models/text-embedding-004",
      "displayName": "Text Embedding 004",
      "inputTokenLimit": 2048,
      "outputTokenLimit": 1,
      "supportedGenerationMethods": ["embedContent", "batchEmbedContents", "predictLongRunning"]
    },
    {
      "name": "",
      "displayName": "skip me"
    }
  ],
  "nextPageToken": "ignore"
}`

func TestParseGoogleListModels_standardJSON(t *testing.T) {
	models, err := parseGoogleListModels([]byte(listModelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("len=%d", len(models))
	}
	flash := models[0]
	if flash.ID != "gemini-2.5-flash" || flash.DisplayName != "Gemini 2.5 Flash" {
		t.Fatalf("flash=%+v", flash)
	}
	if flash.InputTokenLimit != 1048576 || flash.OutputTokenLimit != 8192 {
		t.Fatalf("limits=%+v", flash)
	}
	assertMethods(t, flash.Methods, "countTokens", "generateContent", "streamGenerateContent")
	for _, method := range flash.Methods {
		if strings.Contains(strings.ToLower(method), "bidi") || strings.Contains(strings.ToLower(method), "image") {
			t.Fatalf("media/bidi leaked: %v", flash.Methods)
		}
	}
	embed := models[1]
	if embed.ID != "text-embedding-004" {
		t.Fatalf("embed=%+v", embed)
	}
	assertMethods(t, embed.Methods, "batchEmbedContents", "embedContent")
	for _, method := range embed.Methods {
		if method == "generateContent" || method == "countTokens" {
			t.Fatalf("embedding model claimed generation: %v", embed.Methods)
		}
	}
}

func TestParseGoogleListModels_malformed(t *testing.T) {
	if _, err := parseGoogleListModels([]byte(`not json`)); err == nil {
		t.Fatal("expected error")
	}
	if _, err := parseGoogleListModels(nil); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestCheckBuildAppMethod(t *testing.T) {
	models, err := parseGoogleListModels([]byte(listModelsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBuildAppMethod(models, "models/gemini-2.5-flash", "generateContent"); err != nil {
		t.Fatal(err)
	}
	if err := CheckBuildAppMethod(models, "gemini-2.5-flash", "streamGenerateContent"); err != nil {
		t.Fatal(err)
	}
	if err := CheckBuildAppMethod(models, "text-embedding-004", "embedContent"); err != nil {
		t.Fatal(err)
	}
	if err := CheckBuildAppMethod(models, "text-embedding-004", "batchEmbedContents"); err != nil {
		t.Fatal(err)
	}
	err = CheckBuildAppMethod(models, "gemini-2.5-flash", "embedContent")
	if !errors.Is(err, ErrBuildAppModelNotAvailable) {
		t.Fatalf("method reject=%v", err)
	}
	err = CheckBuildAppMethod(models, "missing-model", "generateContent")
	if !errors.Is(err, ErrBuildAppModelNotAvailable) || !strings.Contains(err.Error(), "missing-model") {
		t.Fatalf("unknown model=%v", err)
	}
	err = CheckBuildAppMethod(nil, "gemini-2.5-flash", "generateContent")
	if !errors.Is(err, ErrBuildAppModelNotAvailable) {
		t.Fatalf("empty catalog=%v", err)
	}
}

func TestBuildAppCatalogCache_ttlAndFailClosed(t *testing.T) {
	var fetches atomic.Int32
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	cache := newBuildAppCatalogCache(func(context.Context, string) ([]byte, error) {
		n := fetches.Add(1)
		if n == 1 {
			return []byte(listModelsFixture), nil
		}
		return nil, errors.New("upstream down")
	})
	cache.now = func() time.Time { return now }
	prevTTL := buildAppCatalogTTL
	buildAppCatalogTTL = time.Hour
	t.Cleanup(func() { buildAppCatalogTTL = prevTTL })

	ctx := context.Background()
	models, err := cache.models(ctx, "acc-build")
	if err != nil || len(models) != 2 {
		t.Fatalf("first fetch models=%d err=%v", len(models), err)
	}
	_, err = cache.models(ctx, "acc-build")
	if err != nil || fetches.Load() != 1 {
		t.Fatalf("cached refetch fetches=%d err=%v", fetches.Load(), err)
	}

	now = now.Add(time.Hour + time.Second)
	_, err = cache.models(ctx, "acc-build")
	if !errors.Is(err, ErrBuildAppCatalogUnavailable) {
		t.Fatalf("expired fail-closed=%v", err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("expired fetches=%d", fetches.Load())
	}
	_, err = cache.models(ctx, "acc-build")
	if !errors.Is(err, ErrBuildAppCatalogUnavailable) || fetches.Load() != 2 {
		t.Fatalf("error cache fetches=%d err=%v", fetches.Load(), err)
	}
	info := cache.info("acc-build")
	if info.Size != 0 || info.Err == nil {
		t.Fatalf("info=%+v", info)
	}
}

func TestBuildAppCatalogCache_singleFlight(t *testing.T) {
	var fetches atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	cache := newBuildAppCatalogCache(func(context.Context, string) ([]byte, error) {
		if fetches.Add(1) == 1 {
			close(started)
			<-release
		}
		return []byte(listModelsFixture), nil
	})
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cache.models(ctx, "acc-build")
			errs <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("stampede fetches=%d", fetches.Load())
	}
}

func assertMethods(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("methods=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("methods=%v want %v", got, want)
		}
	}
}
