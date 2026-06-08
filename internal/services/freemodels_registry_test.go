package services

import (
	"testing"
)

func TestLookupMetaRawNormalizedPrefix(t *testing.T) {
	r := &FreeModelsRegistry{}
	r.replaceIndex(freeModelsEnvelope{Models: []FreeModelMeta{
		{Provider: "google", ModelID: "models/gemini-2.5-flash", PerformanceLevel: "high", EstimatedLatency: "< 1s"},
		{Provider: "nvidia", ModelID: "nvidia/llama-3.1-nemotron-ultra-253b-v1", PerformanceLevel: "mid", EstimatedLatency: "1-3s"},
		{Provider: "groq", ModelID: "llama-3.3-70b-versatile", PerformanceLevel: "high", EstimatedLatency: "< 1s"},
	}})
	// 无前缀 RealModel 应命中带 models/ 前缀的 registry 条目
	if p, l, ok := r.LookupMetaRaw("gemini-2.5-flash"); !ok || p != "high" || l != "< 1s" {
		t.Fatalf("normalized lookup gemini = (%q,%q,%v), want (high,< 1s,true)", p, l, ok)
	}
	// nvidia/ 内部前缀
	if p, _, ok := r.LookupMetaRaw("llama-3.1-nemotron-ultra-253b-v1"); !ok || p != "mid" {
		t.Fatalf("normalized lookup nemotron = (%q,_,%v), want (mid,true)", p, ok)
	}
	// exact 命中仍工作（无前缀模型）
	if p, _, ok := r.LookupMetaRaw("llama-3.3-70b-versatile"); !ok || p != "high" {
		t.Fatalf("exact lookup = (%q,_,%v), want (high,true)", p, ok)
	}
	// 完全不存在的
	if _, _, ok := r.LookupMetaRaw("nonexistent-model-xyz"); ok {
		t.Fatal("unknown model should not be found")
	}
}
