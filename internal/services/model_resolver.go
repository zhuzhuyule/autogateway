package services

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"autogateway/internal/models"
)

// ModelCandidate 一个候选项: 在哪个 sub-group 上, 用什么 real_model 调上游.
// P4 智能路由的转发单元 -- 选中后改写 body.model 为 RealModel, 转发到 GroupID
// 对应的 sub-group. 多个 candidate 形成 ordered pool, 第一个失败 fallback 下一个.
type ModelCandidate struct {
	GroupID   uint   // sub-group ID (用于 GroupManager.GetGroupByName + key lookup)
	GroupName string // sub-group name (logging + GetGroupByName)
	RealModel string // 转发时 body.model 改成这个字符串
	Weight    int    // SWRR weight (alias.Weight 或 GroupSubGroup.Weight)
	Priority  int    // alias.Priority, family 候选默认 100
	Source    string // "alias" | "family" -- 用于 log / metric 区分来源
}

// ModelResolver 把用户提供的 model name 解析成 sub-group candidate 候选池.
// 三层优先级链 alias > family > raw. 第一个命中即返回, 不混合多个来源.
//
// 设计思路: 不修改现有 SubGroupManager / SWRR / fallback 机制. resolve 出
// candidates 后, proxy entry 用第一个 candidate 选 sub-group + 改写 body.model.
// fallback 时 proxy 从 candidate pool 取下一个; pool 耗尽才走老 raw id 字符串
// 匹配 fallback.
type ModelResolver struct {
	aliasSvc *AliasService
	freeReg  *FreeModelsRegistry
	groupMgr *GroupManager
}

func NewModelResolver(aliasSvc *AliasService, freeReg *FreeModelsRegistry, groupMgr *GroupManager) *ModelResolver {
	return &ModelResolver{
		aliasSvc: aliasSvc,
		freeReg:  freeReg,
		groupMgr: groupMgr,
	}
}

// Resolve 返回候选池, 仅 aggregate group 启用. nil 表示未命中 (调用方走 fallback 路径).
// 空 slice 也表示无候选 (alias/family 命中但都找不到对应 sub-group), 走 fallback.
func (r *ModelResolver) Resolve(ctx context.Context, aggregate *models.Group, userModel string) []ModelCandidate {
	if aggregate == nil || aggregate.GroupType != "aggregate" {
		return nil
	}
	userModel = strings.TrimSpace(userModel)
	if userModel == "" {
		return nil
	}

	// 1. Alias 表 (人工声明, 最高优先级 -- admin 显式映射意图最强)
	if r.aliasSvc != nil {
		rows, err := r.aliasSvc.ListEnabledByAlias(ctx, userModel)
		if err == nil && len(rows) > 0 {
			out := make([]ModelCandidate, 0, len(rows))
			subGroupIDs := r.subGroupIDSet(aggregate)
			for _, a := range rows {
				// alias.GroupID 必须在 aggregate.SubGroups 范围内, 否则
				// 命中 alias 但路由到 aggregate 外的 group 是 leak.
				if _, ok := subGroupIDs[a.GroupID]; !ok {
					continue
				}
				name, _ := r.groupMgr.GetGroupNameByID(a.GroupID)
				if name == "" {
					continue
				}
				out = append(out, ModelCandidate{
					GroupID:   a.GroupID,
					GroupName: name,
					RealModel: a.RealModel,
					Weight:    a.Weight,
					Priority:  a.Priority,
					Source:    "alias",
				})
			}
			if len(out) > 0 {
				return out
			}
		}
	}

	// 2. FreeModels Family (自动识别, 跨 provider 等价类)
	if r.freeReg != nil {
		metas := r.freeReg.ListByFamily(userModel)
		if len(metas) > 0 {
			return r.familyToCandidates(aggregate, metas)
		}
	}

	// 3. 都没命中 → nil → 调用方走老 raw id 字符串匹配 (SubGroupManager.SelectSubGroupForModel)
	return nil
}

// subGroupIDSet aggregate 下所有 sub-group ID 的集合, 用来约束 alias 路由不出框.
func (r *ModelResolver) subGroupIDSet(aggregate *models.Group) map[uint]struct{} {
	set := make(map[uint]struct{}, len(aggregate.SubGroups))
	for _, sg := range aggregate.SubGroups {
		set[sg.SubGroupID] = struct{}{}
	}
	return set
}

// familyToCandidates 把 FreeModels family 候选映射到 aggregate 下实际的 sub-group.
// 匹配规则: sub-group 的 upstream host 反查得到的 providerID == meta.Provider.
// 同一 provider 多个 sub-group (e.g. groq-1 / groq-2 同 host) 都会进候选.
func (r *ModelResolver) familyToCandidates(aggregate *models.Group, metas []FreeModelMeta) []ModelCandidate {
	// 按 family meta 的 provider 分组, 加速后续匹配
	metaByProvider := make(map[string][]FreeModelMeta, len(metas))
	for _, m := range metas {
		p := strings.ToLower(m.Provider)
		metaByProvider[p] = append(metaByProvider[p], m)
	}

	out := make([]ModelCandidate, 0, len(metas))
	for _, sg := range aggregate.SubGroups {
		subGroup, err := r.groupMgr.GetGroupByName(sg.SubGroupName)
		if err != nil || subGroup == nil {
			continue
		}
		providerID := r.resolveProviderID(subGroup)
		if providerID == "" {
			continue
		}
		matched, ok := metaByProvider[strings.ToLower(providerID)]
		if !ok {
			continue
		}
		// 同 provider 可能有多个 family meta (e.g. openrouter 的 :free + 非 :free 变体),
		// 但通常 family 内只有一个 raw id 跟 provider 对应. 取第一个.
		for _, m := range matched {
			out = append(out, ModelCandidate{
				GroupID:   subGroup.ID,
				GroupName: subGroup.Name,
				RealModel: m.ModelID,
				Weight:    sg.Weight,
				Priority:  100, // family 候选默认 priority
				Source:    "family",
			})
		}
	}
	return out
}

// resolveProviderID 从 group 的第一个 upstream URL 反查 FreeModels provider ID.
// 跟 model_catalog_handler.go 的 resolveFreeProviderHint 等价 -- 复制一份避免
// services / handler 循环依赖. fallback 用 group.Name (常跟 provider id 重叠).
func (r *ModelResolver) resolveProviderID(g *models.Group) string {
	if r.freeReg != nil && len(g.Upstreams) > 0 {
		var defs []struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(g.Upstreams, &defs); err == nil && len(defs) > 0 {
			if u, perr := url.Parse(defs[0].URL); perr == nil && u.Hostname() != "" {
				if id, _, ok := r.freeReg.LookupProviderByHost(u.Hostname()); ok {
					return id
				}
			}
		}
	}
	return g.Name
}
