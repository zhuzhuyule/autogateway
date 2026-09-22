# 子分组优先级 (priority)

聚合分组的子分组有两个选路维度:

| 维度 | 作用 | 默认 |
|---|---|---|
| `priority` | **层级**。数值越小越优先。 | 100 |
| `weight` | **同层内**的流量比例。 | 1 |

一句话:**priority 决定"用哪一层", weight 决定"这一层里怎么分"。**

---

## 语义

选路分两步:

1. 只看**当前可用的最优先层**(数值最小的那一层), 在层内按 weight 做
   SWRR(平滑加权轮询, 且带延迟自适应 —— 慢的子分组会被自动降权)。
2. 该层**全部不可用**时, 才降到下一层。

"不可用"的判定有三种:

- 该子分组处于熔断冷却期(连续失败触发的 circuit breaker)
- 该子分组没有 active 状态的密钥
- 该子分组不声明支持当前请求的模型(聚合层严格路由)

所以 priority 能表达"主用 / 备用"的层级关系:

```
priority=10   主力 provider       ← 正常情况下全量流量
priority=20   备用 provider       ← 主力挂了才接管
priority=100  兜底 provider       ← 前面都没了才用
```

---

## 与 weight 的配合

```
A: priority=10, weight=3
B: priority=10, weight=1
C: priority=20, weight=5
```

- 正常情况:A 拿 75%, B 拿 25%, **C 完全拿不到流量**
- A 和 B 都熔断/无 key 时:C 接管 100%(此时它是唯一的可用层)

想让 C 平时也分到一点流量 → 把它的 priority 改成 10, 用 weight 调比例。
**priority 不是"权重微调", 是硬分层。**

---

## 存量配置零影响

默认 100。所有子分组的 priority 相同时, 选路**完全退化为原来的纯 SWRR** ——
既不额外查存储, 也不改变任何既有行为。

也就是说: 不配 priority, 一切和以前一样。

---

## 怎么配

### 管理面板

- **添加子分组**弹窗: 每个子分组有 weight 和 priority 两个输入框
- **子分组表格**: 权重旁边显示 `p 100` 徽标(鼠标悬停有说明)
- **编辑权重与优先级**弹窗: 两者都能改

### API

添加子分组(`priority` 可选, 不传则 100):

```bash
curl -X POST http://localhost:3001/api/groups/3/sub-groups \
  -H "Authorization: Bearer $AUTH_KEY" -H "Content-Type: application/json" \
  -d '{"sub_groups":[
        {"group_id":8,  "weight":3, "priority":10},
        {"group_id":12, "weight":5, "priority":20}
      ]}'
```

单独更新(`priority` 可选, 不传表示**只改 weight**):

```bash
curl -X PUT http://localhost:3001/api/groups/3/sub-groups/8/weight \
  -H "Authorization: Bearer $AUTH_KEY" -H "Content-Type: application/json" \
  -d '{"weight":3,"priority":10}'
```

查询响应里会带 `priority`:

```bash
curl http://localhost:3001/api/groups/3/sub-groups -H "Authorization: Bearer $AUTH_KEY"
# data[].priority
```

**取值范围** 1–1000。负数会被拒绝; `0` 视为"未设置", 落库时写成默认 100。

---

## 与模型别名 priority 的区别

两者**语义不同**, 别混:

| | 子分组 priority | 别名 priority |
|---|---|---|
| 语义 | **分层**(层级间互斥) | **tie-break**(仅 weight 打平时生效) |
| 主序 | priority 优先, weight 次之 | weight 优先, priority 次之 |

为什么不一样: 别名解决的是"同一个对外模型名映射到多个上游真名", 天然是
比例分配问题; 子分组解决的是"流量该走哪个 provider", 需要表达主备层级。

---

## 实现位置

- 字段与语义: `internal/models/types.go` 的 `GroupSubGroup.Priority`
- 选路: `internal/services/subgroup_manager.go` 的
  `selectAmong` / `bestEligibleTier` / `hasDistinctPriorities`
- 迁移: `internal/db/migrations/v2_7_1_GroupSubGroupPriority.go`
- 测试: `internal/services/subgroup_routing_test.go`
