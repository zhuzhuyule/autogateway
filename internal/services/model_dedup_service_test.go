package services

import (
	"reflect"
	"testing"

	"autogateway/internal/models"

	"gorm.io/datatypes"
)

// 聚合分组 /v1/models 的核心契约: 返回所有子分组"可调模型"的并集.
// specified 子分组用 exposed_models 白名单(不泄漏 available 里未暴露的),
// passthrough 用 available_models, 各自剔除 blocked_models, 全局去重 + 排序.
func TestAggregateCandidateModelIDs_UnionsAllSubGroups(t *testing.T) {
	specified := &models.Group{
		ModelRoutingMode: "specified",
		ExposedModels:    datatypes.JSON(`["gpt-4","gpt-4o"]`),
		// available 里有更多, 但 specified 只应暴露 exposed 白名单
		AvailableModels: datatypes.JSON(`["gpt-4","gpt-4o","gpt-3.5-turbo","legacy-davinci"]`),
	}
	passthrough := &models.Group{
		ModelRoutingMode: "passthrough",
		// gpt-4o 与 specified 重叠, 应去重
		AvailableModels: datatypes.JSON(`["claude-3-opus","gpt-4o"]`),
	}
	withBlocked := &models.Group{
		ModelRoutingMode: "passthrough",
		AvailableModels:  datatypes.JSON(`["gemini-1.5-pro","banned-model"]`),
		BlockedModels:    datatypes.JSON(`["banned-model"]`),
	}

	got := aggregateCandidateModelIDs([]*models.Group{specified, passthrough, withBlocked, nil})
	want := []string{"claude-3-opus", "gemini-1.5-pro", "gpt-4", "gpt-4o"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate union mismatch:\n got  = %v\n want = %v", got, want)
	}
}

// specified 但 exposed 为空 → fallback 到 available_models (与 picker/路由降级一致).
func TestAggregateCandidateModelIDs_SpecifiedFallsBackWhenExposedEmpty(t *testing.T) {
	g := &models.Group{
		ModelRoutingMode: "specified",
		ExposedModels:    datatypes.JSON(`[]`),
		AvailableModels:  datatypes.JSON(`["m1","m2"]`),
	}
	got := aggregateCandidateModelIDs([]*models.Group{g})
	want := []string{"m1", "m2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback mismatch:\n got  = %v\n want = %v", got, want)
	}
}

func TestAggregateCandidateModelIDs_Empty(t *testing.T) {
	if got := aggregateCandidateModelIDs(nil); len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}
