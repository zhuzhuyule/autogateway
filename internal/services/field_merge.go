package services

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"autogateway/internal/models"
	"gorm.io/datatypes"
)

// jsonMapEqual 按内容比较两个 JSONMap。json.Marshal 对 map 键排序, 因此与插入
// 顺序无关; 相同内容返回 true。
func jsonMapEqual(a, b datatypes.JSONMap) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// sameGroupJSONColumns 判断 base 的四个 JSON 字段(三列 + ConfigVersions)是否
// 与 existing 完全一致 —— 用于在"本端 record 赢"时避免无变化的伪写入引起
// updated_at 抖动 / 同步 churn。
func sameGroupJSONColumns(existing, base *models.Group) bool {
	return jsonMapEqual(existing.Config, base.Config) &&
		jsonMapEqual(existing.ParamOverrides, base.ParamOverrides) &&
		jsonMapEqual(existing.ModelRedirectRules, base.ModelRedirectRules) &&
		jsonMapEqual(existing.ConfigVersions, base.ConfigVersions)
}

// groupJSONColumns 是 Group 上按字段级 LWW 同步的三个 JSON 列。
// ConfigVersions 的键形如 "<column>.<field>"。
var groupJSONColumns = []string{"config", "param_overrides", "model_redirect_rules"}

// toInt64Version 把 ConfigVersions 里的版本值转成 int64。不同来源类型都要兼容:
// 内存里是 int/int64; JSON 数字反序列化成 float64; 而 sqlite 等 JSON 列往返后
// 可能回吐成 string / json.Number。任何无法解析的值当 0(即"无版本")。
func toInt64Version(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}

// sliceColumnVersions 从 "<column>.<field>" 形式的 ConfigVersions 里, 抽出某个
// 列的裸字段版本表。
func sliceColumnVersions(cv datatypes.JSONMap, column string) map[string]int64 {
	out := map[string]int64{}
	prefix := column + "."
	for k, v := range cv {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = toInt64Version(v)
		}
	}
	return out
}

// joinColumnVersions 把某列合并后的裸字段版本表, 以 "<column>.<field>" 写回
// 汇总的 versions map。
func joinColumnVersions(dst datatypes.JSONMap, column string, vers map[string]int64) {
	for f, v := range vers {
		dst[column+"."+f] = v
	}
}

// jsonValEqual 按 JSON 序列化比较两个标量字段值是否相等。
func jsonValEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// stampConfigVersions 比较某列的旧/新内容, 对新增 / 修改 / 删除的字段, 把
// versions["<column>.<field>"] 盖成 nowMillis; 未变的字段保留旧版本。
// 本地写入(创建/更新分组)时调用, 让删除也带上"更新的版本"以便同步胜出。
func stampConfigVersions(
	versions datatypes.JSONMap,
	column string,
	oldCol, newCol datatypes.JSONMap,
	nowMillis int64,
) {
	fields := map[string]struct{}{}
	for k := range oldCol {
		fields[k] = struct{}{}
	}
	for k := range newCol {
		fields[k] = struct{}{}
	}
	for f := range fields {
		ov, oOK := oldCol[f]
		nv, nOK := newCol[f]
		changed := oOK != nOK || (oOK && nOK && !jsonValEqual(ov, nv))
		if changed {
			versions[column+"."+f] = nowMillis
		}
	}
}

// mergeGroupJSONColumns 对 existing/incoming 两条 Group 的三个 JSON 列做字段级
// LWW 合并, 返回合并后的三列与汇总的 ConfigVersions。
//
// 某列两侧都没有字段版本(升级前的老数据)时, 该列回退到记录级 LWW:
// recordWinnerIncoming 决定用哪一侧整列的值, 保证零回归。
func mergeGroupJSONColumns(
	existing, incoming *models.Group,
	recordWinnerIncoming bool,
) (cfg, pov, mrr, versions datatypes.JSONMap) {
	versions = datatypes.JSONMap{}

	merge := func(colName string, eMap, iMap datatypes.JSONMap) datatypes.JSONMap {
		eVers := sliceColumnVersions(existing.ConfigVersions, colName)
		iVers := sliceColumnVersions(incoming.ConfigVersions, colName)
		if len(eVers) == 0 && len(iVers) == 0 {
			if recordWinnerIncoming {
				return iMap
			}
			return eMap
		}
		mergedMap, mergedVers := mergeJSONFieldsLWW(
			map[string]any(eMap), map[string]any(iMap), eVers, iVers,
		)
		joinColumnVersions(versions, colName, mergedVers)
		return datatypes.JSONMap(mergedMap)
	}

	cfg = merge("config", existing.Config, incoming.Config)
	pov = merge("param_overrides", existing.ParamOverrides, incoming.ParamOverrides)
	mrr = merge("model_redirect_rules", existing.ModelRedirectRules, incoming.ModelRedirectRules)
	return cfg, pov, mrr, versions
}

// mergeJSONFieldsLWW 对一个 JSON 配置列做逐字段 last-write-wins 合并。
//
// 每个字段带自己的版本时间戳 (毫秒, 见 existingVers/incomingVers): 版本新的
// 一方决定该字段的取值。字段"缺失 + 版本更新" = 一次胜出的删除 —— 这让删除
// 能压过对端仍留着的旧值, 修复"删字段被同步还原"。
//
// 返回合并后的字段 map 与合并后的版本 map (逐字段取 max)。纯函数, 无副作用。
func mergeJSONFieldsLWW(
	existing, incoming map[string]any,
	existingVers, incomingVers map[string]int64,
) (map[string]any, map[string]int64) {
	merged := map[string]any{}
	mergedVers := map[string]int64{}

	fields := map[string]struct{}{}
	for k := range existing {
		fields[k] = struct{}{}
	}
	for k := range incoming {
		fields[k] = struct{}{}
	}
	for k := range existingVers {
		fields[k] = struct{}{}
	}
	for k := range incomingVers {
		fields[k] = struct{}{}
	}

	for f := range fields {
		ve := existingVers[f]
		vi := incomingVers[f]

		var (
			val     any
			present bool
			winVer  int64
		)
		switch {
		case vi > ve:
			val, present = incoming[f]
			winVer = vi
		case ve > vi:
			val, present = existing[f]
			winVer = ve
		default:
			// 平局: 版本相同。对称裁决 —— present 胜过缺失(删除只在严格更新时才赢);
			// 两侧都有值则按值确定性取(较大者), 保证 A 合 B 与 B 合 A 结果一致, 不分叉。
			eVal, ep := existing[f]
			iVal, ip := incoming[f]
			winVer = ve // == vi
			switch {
			case ep && !ip:
				val, present = eVal, true
			case ip && !ep:
				val, present = iVal, true
			case ep && ip:
				if fmt.Sprintf("%v", iVal) > fmt.Sprintf("%v", eVal) {
					val, present = iVal, true
				} else {
					val, present = eVal, true
				}
			}
		}
		if present {
			merged[f] = val
		}
		if winVer > 0 {
			mergedVers[f] = winVer
		}
	}
	return merged, mergedVers
}
