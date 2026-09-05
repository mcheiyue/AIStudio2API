package aistudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrBuildAppCatalogUnavailable 表示 Build 目录拉取失败或尚未成功，校验 fail-closed。
	ErrBuildAppCatalogUnavailable = errors.New("buildapp catalog unavailable")
	// ErrBuildAppModelNotAvailable 表示模型不在该 Build 账号目录中，或不支持请求方法。
	ErrBuildAppModelNotAvailable = errors.New("model not available for buildapp account")
)

const defaultBuildAppCatalogTTL = time.Hour

// ponytail: package-level TTL so tests can shorten expiry without a clock interface.
var buildAppCatalogTTL = defaultBuildAppCatalogTTL

var buildAppTrackedMethods = map[string]struct{}{
	"generateContent":       {},
	"streamGenerateContent": {},
	"countTokens":           {},
	"embedContent":          {},
	"batchEmbedContents":    {},
}

type buildAppCatalogEntry struct {
	models    []BuildAppModel
	fetchedAt time.Time
	err       error
}

type buildAppCatalogCache struct {
	mu       sync.Mutex
	entries  map[string]buildAppCatalogEntry
	inflight map[string]chan struct{}
	now      func() time.Time
	fetch    func(context.Context, string) ([]byte, error)
}

func newBuildAppCatalogCache(fetch func(context.Context, string) ([]byte, error)) *buildAppCatalogCache {
	return &buildAppCatalogCache{
		entries:  make(map[string]buildAppCatalogEntry),
		inflight: make(map[string]chan struct{}),
		now:      time.Now,
		fetch:    fetch,
	}
}

func (c *buildAppCatalogCache) models(ctx context.Context, accountID string) ([]BuildAppModel, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || c == nil || c.fetch == nil {
		return nil, ErrBuildAppCatalogUnavailable
	}
	c.mu.Lock()
	if entry, ok := c.entries[accountID]; ok && c.now().Sub(entry.fetchedAt) < buildAppCatalogTTL {
		models := cloneBuildAppModels(entry.models)
		err := entry.err
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return models, nil
	}
	if wait, ok := c.inflight[accountID]; ok {
		c.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		c.mu.Lock()
		entry := c.entries[accountID]
		c.mu.Unlock()
		if entry.err != nil {
			return nil, entry.err
		}
		return cloneBuildAppModels(entry.models), nil
	}
	done := make(chan struct{})
	c.inflight[accountID] = done
	c.mu.Unlock()

	entry := c.refresh(ctx, accountID)

	c.mu.Lock()
	c.entries[accountID] = entry
	delete(c.inflight, accountID)
	close(done)
	c.mu.Unlock()

	if entry.err != nil {
		return nil, entry.err
	}
	return cloneBuildAppModels(entry.models), nil
}

func (c *buildAppCatalogCache) refresh(ctx context.Context, accountID string) buildAppCatalogEntry {
	raw, err := c.fetch(ctx, accountID)
	if err != nil {
		return buildAppCatalogEntry{fetchedAt: c.now(), err: fmt.Errorf("%w: %v", ErrBuildAppCatalogUnavailable, err)}
	}
	models, err := parseGoogleListModels(raw)
	if err != nil {
		return buildAppCatalogEntry{fetchedAt: c.now(), err: fmt.Errorf("%w: %v", ErrBuildAppCatalogUnavailable, err)}
	}
	return buildAppCatalogEntry{models: models, fetchedAt: c.now()}
}

func (c *buildAppCatalogCache) info(accountID string) BuildAppCatalogInfo {
	if c == nil {
		return BuildAppCatalogInfo{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[strings.TrimSpace(accountID)]
	if !ok {
		return BuildAppCatalogInfo{}
	}
	return BuildAppCatalogInfo{Size: len(entry.models), FetchedAt: entry.fetchedAt, Err: entry.err}
}

type googleListModelsResponse struct {
	Models []googleListModel `json:"models"`
}

type googleListModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int64    `json:"inputTokenLimit"`
	OutputTokenLimit           int64    `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

func parseGoogleListModels(raw []byte) ([]BuildAppModel, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty ListModels body")
	}
	var payload googleListModelsResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode ListModels: %w", err)
	}
	models := make([]BuildAppModel, 0, len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Models))
	for _, item := range payload.Models {
		model, ok := googleModelToBuildApp(item)
		if !ok {
			continue
		}
		if _, dup := seen[model.ID]; dup {
			continue
		}
		seen[model.ID] = struct{}{}
		models = append(models, applyBuildAppMethodBaseline(model))
	}
	return models, nil
}

func googleModelToBuildApp(item googleListModel) (BuildAppModel, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
	if id == "" {
		return BuildAppModel{}, false
	}
	methods := make([]string, 0, len(item.SupportedGenerationMethods))
	for _, method := range item.SupportedGenerationMethods {
		method = strings.TrimSpace(method)
		if _, ok := buildAppTrackedMethods[method]; ok {
			methods = append(methods, method)
		}
	}
	name := strings.TrimSpace(item.DisplayName)
	if name == "" {
		name = id
	}
	return BuildAppModel{
		ID:               id,
		DisplayName:      name,
		Description:      item.Description,
		InputTokenLimit:  item.InputTokenLimit,
		OutputTokenLimit: item.OutputTokenLimit,
		Methods:          methods,
	}, true
}

func applyBuildAppMethodBaseline(model BuildAppModel) BuildAppModel {
	set := make(map[string]struct{}, len(model.Methods)+4)
	for _, method := range model.Methods {
		set[method] = struct{}{}
	}
	embed := hasBuildAppMethod(set, "embedContent") || hasBuildAppMethod(set, "batchEmbedContents") ||
		strings.Contains(strings.ToLower(model.ID), "embedding")
	if embed {
		set["embedContent"] = struct{}{}
		set["batchEmbedContents"] = struct{}{}
	} else {
		set["countTokens"] = struct{}{}
		if hasBuildAppMethod(set, "generateContent") || hasBuildAppMethod(set, "streamGenerateContent") {
			set["generateContent"] = struct{}{}
			set["streamGenerateContent"] = struct{}{}
		}
	}
	model.Methods = sortedMethodSet(set)
	return model
}

func hasBuildAppMethod(set map[string]struct{}, method string) bool {
	_, ok := set[method]
	return ok
}

func sortedMethodSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for method := range set {
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

// CheckBuildAppMethod 校验模型存在且允许该方法；失败返回 typed 400 sentinel。
func CheckBuildAppMethod(models []BuildAppModel, modelID string, method string) error {
	modelID = strings.TrimPrefix(strings.TrimSpace(modelID), "models/")
	method = strings.TrimSpace(method)
	if modelID == "" {
		return fmt.Errorf("%w: %s", ErrBuildAppModelNotAvailable, modelID)
	}
	for _, model := range models {
		if model.ID != modelID {
			continue
		}
		if buildAppMethodAllowed(model.Methods, method) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrBuildAppModelNotAvailable, modelID)
	}
	return fmt.Errorf("%w: %s", ErrBuildAppModelNotAvailable, modelID)
}

func buildAppMethodAllowed(methods []string, method string) bool {
	aliases := []string{method}
	switch method {
	case "embedContent", "batchEmbedContents":
		aliases = []string{"embedContent", "batchEmbedContents"}
	case "generateContent", "streamGenerateContent":
		aliases = []string{"generateContent", "streamGenerateContent"}
	}
	for _, candidate := range aliases {
		for _, have := range methods {
			if have == candidate {
				return true
			}
		}
	}
	return false
}

func cloneBuildAppModels(models []BuildAppModel) []BuildAppModel {
	out := make([]BuildAppModel, len(models))
	for i, model := range models {
		out[i] = model
		out[i].Methods = append([]string(nil), model.Methods...)
	}
	return out
}

// ToModel 把 Build 目录项映射为对外 Model JSON 使用的形状（不带 Playground capability 码）。
func (m BuildAppModel) ToModel() Model {
	return Model{
		ID:               m.ID,
		Name:             m.DisplayName,
		Description:      m.Description,
		Methods:          append([]string(nil), m.Methods...),
		InputTokenLimit:  m.InputTokenLimit,
		OutputTokenLimit: m.OutputTokenLimit,
	}
}

type catalogCapture struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (c *catalogCapture) Header() http.Header {
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}

func (c *catalogCapture) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.buf.Write(p)
}

func (c *catalogCapture) WriteHeader(status int) {
	c.code = status
}

func (p *AccountPool) relayBuildAppListModels(ctx context.Context, accountID string) ([]byte, error) {
	worker, err := p.BuildAppWorker(ctx, accountID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://buildapp.local/v1beta/models", nil)
	if err != nil {
		return nil, err
	}
	rec := &catalogCapture{header: make(http.Header)}
	worker.ServeHTTP(rec, req)
	if rec.code != 0 && rec.code != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", rec.code, rec.buf.String())
	}
	return rec.buf.Bytes(), nil
}

// BuildAppCatalogInfo 返回缓存摘要，不触发刷新。
func (p *AccountPool) BuildAppCatalogInfo(accountID string) BuildAppCatalogInfo {
	if p == nil {
		return BuildAppCatalogInfo{}
	}
	return p.buildappCatalog.info(accountID)
}

// BuildAppModels 实现 BuildAppCatalog：TTL 缓存的独立 Build 目录。
func (s *PooledService) BuildAppModels(ctx context.Context, accountID string) ([]BuildAppModel, error) {
	if s == nil || s.pool == nil {
		return nil, ErrBuildAppCatalogUnavailable
	}
	return s.pool.buildappCatalog.models(ctx, accountID)
}

// BuildAppCatalogInfo 实现 BuildAppCatalog。
func (s *PooledService) BuildAppCatalogInfo(accountID string) BuildAppCatalogInfo {
	if s == nil || s.pool == nil {
		return BuildAppCatalogInfo{}
	}
	return s.pool.BuildAppCatalogInfo(accountID)
}
