package services

import (
	"testing"

	"gorm.io/datatypes"
)

// RED #4: 本地写入盖版本 —— 删除字段要盖上新版本(让它能压过对端旧值),
// 未变字段保留旧版本(不引起无谓 churn)。
func TestStampConfigVersions_DeletionStampsField(t *testing.T) {
	versions := datatypes.JSONMap{"config.a": int64(50), "config.b": int64(50)}
	oldCol := datatypes.JSONMap{"a": 1, "b": 2}
	newCol := datatypes.JSONMap{"a": 1} // b 被删

	stampConfigVersions(versions, "config", oldCol, newCol, 999)

	if toInt64Version(versions["config.b"]) != 999 {
		t.Fatalf("删除的 b 应盖版本 999, 得到 %v", versions["config.b"])
	}
	if toInt64Version(versions["config.a"]) != 50 {
		t.Fatalf("未变的 a 应保留旧版本 50, 得到 %v", versions["config.a"])
	}
}

// mergeJSONFieldsLWW 是字段级 LWW 合并的纯函数核心:
// 每个字段带自己的版本时间戳 (毫秒), 谁的版本新用谁的值;
// "缺失 + 版本更新" = 一次胜出的删除。

// RED #1: 删除(缺失 + 更新的版本)压过对端的旧值 —— 这正是"删字段被同步还原"的场景。
func TestMergeJSONFieldsLWW_DeletionWithNewerVersionWins(t *testing.T) {
	existing := map[string]any{"rpm_limit": 500}       // 对端仍留着旧值
	existingVers := map[string]int64{"rpm_limit": 100} // 旧版本
	incoming := map[string]any{}                       // 本端已删除该字段
	incomingVers := map[string]int64{"rpm_limit": 200} // 删除事件, 更新的版本

	merged, mergedVers := mergeJSONFieldsLWW(existing, incoming, existingVers, incomingVers)

	if _, ok := merged["rpm_limit"]; ok {
		t.Fatalf("rpm_limit 应保持删除(版本更新的一方是删除), 却得到 %v", merged["rpm_limit"])
	}
	if mergedVers["rpm_limit"] != 200 {
		t.Fatalf("合并后版本应为 200, 得到 %d", mergedVers["rpm_limit"])
	}
}

// RED #2: 平局(版本相同)时, present 值胜过缺失, 且结果必须对称
// (A 合 B 与 B 合 A 一致), 否则双向 mesh 会分叉。删除只在"严格更新"时才赢。
func TestMergeJSONFieldsLWW_TiePrefersPresentSymmetric(t *testing.T) {
	present := map[string]any{"max_retries": 3}
	presentVers := map[string]int64{"max_retries": 100}
	absent := map[string]any{}
	absentVers := map[string]int64{"max_retries": 100} // 同版本的删除

	// 方向 1: present 作为 existing
	m1, _ := mergeJSONFieldsLWW(present, absent, presentVers, absentVers)
	// 方向 2: present 作为 incoming (交换两侧)
	m2, _ := mergeJSONFieldsLWW(absent, present, absentVers, presentVers)

	v1, ok1 := m1["max_retries"]
	v2, ok2 := m2["max_retries"]
	if !ok1 || !ok2 {
		t.Fatalf("平局时 present 值应保留, 得到 m1=%v(%v) m2=%v(%v)", v1, ok1, v2, ok2)
	}
	if v1 != v2 {
		t.Fatalf("平局结果必须对称, m1=%v m2=%v", v1, v2)
	}
}
