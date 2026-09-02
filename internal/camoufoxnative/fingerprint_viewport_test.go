package camoufoxnative

import "testing"

func TestViewportExtentRejectsZero(t *testing.T) {
	if got := viewportExtent(0, 1696, 0); got != 1696 {
		t.Fatalf("宽度回退期望 1696，实际 %d", got)
	}
	if got := viewportExtent(0, 1026, chromeHeight); got != 1026-chromeHeight {
		t.Fatalf("高度回退期望 %d，实际 %d", 1026-chromeHeight, got)
	}
	if got := viewportExtent(1380, 1696, 0); got != 1380 {
		t.Fatalf("已有正值应原样保留，实际 %d", got)
	}
	if got := viewportExtent(0, 40, chromeHeight); got != 40 {
		t.Fatalf("外框小于浏览器边框时应回退到外框，实际 %d", got)
	}
}

func TestRepairViewportConfigFixesPersistedZeros(t *testing.T) {
	config := map[string]any{
		"window.outerWidth":  float64(1696),
		"window.outerHeight": float64(1026),
		"window.innerWidth":  float64(0),
		"window.innerHeight": float64(0),
	}
	if !repairViewportConfig(config) {
		t.Fatal("含 0 视口的指纹应被判定为需要修复")
	}
	if got := configInt(config["window.innerWidth"]); got != 1696 {
		t.Fatalf("innerWidth 期望 1696，实际 %d", got)
	}
	if got := configInt(config["window.innerHeight"]); got != 1026-chromeHeight {
		t.Fatalf("innerHeight 期望 %d，实际 %d", 1026-chromeHeight, got)
	}
	if repairViewportConfig(config) {
		t.Fatal("修复后再次调用不应报告改写")
	}
}
