# 参数覆盖 (param_overrides)

按条件改写**出站**请求体。给分组配一份规则, 网关在转发前执行, 上游收到的是
改写后的 body。用来适配那些对字段有特殊要求的 provider, 不必改代码。

配置位置: 分组编辑 → `param_overrides`(JSON)。

---

## 三种格式

按以下优先级判定, **只命中一种**:

| 格式 | 判定条件 | 说明 |
|---|---|---|
| **advanced** | 含 `operations` 键 | 条件 + 定点操作, 本文档的主角 |
| **nested** | 所有值都是对象 | `{"*": {...}, "gpt-5": {...}}`, 按 model 匹配 |
| **flat** | 其余 | 顶层 `key: value` 直接覆盖 |

后两种是历史格式, 行为保持不变。**含 `operations` 时绝不会退回后两种** ——
否则那个数组会被当成普通字段塞进出站 body。

---

## 路径语法

点号分隔, 支持数组下标:

```
temperature              根级字段
messages.0.content       数组第 1 个元素
messages.-1.content      数组最后一个元素
metadata.user.name       嵌套对象
```

不支持通配和切片 —— 覆盖规则是给已知结构做定点修改, 通配会让行为难以预测。

中间层不存在时**报错**, 不自动造结构。

---

## 操作模式

`operations` 按数组顺序**依次执行**, 前面的结果影响后面的操作。

| mode | 必填 | 说明 |
|---|---|---|
| `set` | path, value | 设值。`keep_origin: true` 时已有值则跳过 |
| `delete` | path | 删除字段(数组则移除该元素) |
| `move` | from, to | 移动字段(删源) |
| `copy` | from, to | 复制字段(留源) |
| `append` | path, value | 字符串拼接 / 数组追加 / 对象合并 |
| `prepend` | path, value | 同上, 加到前面 |
| `trim_prefix` | path, value | 去掉前缀(不匹配则不变) |
| `trim_suffix` | path, value | 去掉后缀 |
| `ensure_prefix` | path, value | 确保有前缀(已有则不动) |
| `ensure_suffix` | path, value | 确保有后缀 |
| `trim_space` | path | 去首尾空白 |
| `to_lower` / `to_upper` | path | 大小写转换 |
| `replace` | path, from, to | 子串替换。`to` 可为空(= 删掉) |
| `regex_replace` | path, from, to | 正则替换(Go regexp 语法) |

`append` / `prepend` 按目标类型分派: 字符串→拼接, 数组→追加(值为数组则依次加入),
对象→合并。

---

## 条件

```
{"path": "...", "mode": "contains", "value": "...", "invert": false, "pass_missing_key": false}
```

| mode | 说明 |
|---|---|
| `full`(默认) | 完全相等 |
| `prefix` / `suffix` / `contains` | 字符串匹配 |
| `gt` / `gte` / `lt` / `lte` | 数值比较 |

- `invert`: 取反
- `pass_missing_key`: 路径不存在时是否算通过(默认 `false`, 即不通过)
- `logic`: 多条件间的关系。**`AND`(默认)** 或 `OR`

> ⚠️ 与 New API 的差异: New API 的默认逻辑是 `OR`, 这里是 `AND`。
> 多条件写在一起通常是"都要满足", `AND` 更不容易误触发。

### 内置变量

条件里可直接用, 不需要出现在请求体里:

| 变量 | 含义 |
|---|---|
| `model` | 重定向后的目标模型(即 body 里当前的 model) |
| `upstream_model` | 上游真实模型名(与 `model` 同源) |
| `original_model` | **客户端原本请求**的模型名(alias 改写之前) |

`original_model` 的典型用途: 用同一个上游模型服务多个对外别名, 按别名分流。

---

## 示例

### 1. 给上游补一个 system 提示

```json
{"operations": [
  {"mode": "prepend", "path": "messages",
   "value": {"role": "system", "content": "始终用中文回答。"}}
]}
```

### 2. 上游要求模型名带前缀 + 强制温度

```json
{"operations": [
  {"mode": "ensure_prefix", "path": "model", "value": "openai/"},
  {"mode": "set", "path": "temperature", "value": 0.3}
]}
```

### 3. 按用户请求的别名分流

```json
{"operations": [
  {"mode": "set", "path": "reasoning_effort", "value": "high",
   "conditions": [{"path": "original_model", "mode": "suffix", "value": "-thinking"}]}
]}
```

### 4. 上游不吃某些字段

```json
{"operations": [
  {"mode": "delete", "path": "logprobs"},
  {"mode": "delete", "path": "top_logprobs"}
]}
```

### 5. 条件组合(AND)

```json
{"operations": [
  {"mode": "set", "path": "stream", "value": false,
   "conditions": [
     {"path": "model", "mode": "contains", "value": "claude"},
     {"path": "max_tokens", "mode": "gt", "value": 1000}
   ]}
]}
```

---

## 出错行为

**任何一步失败 → 整个改写放弃, 原 body 原样转发**, 并记录一条错误日志。

理由: 给上游发一个"改了一半"的请求体, 比不改更难排查。所以规则要么整体生效,
要么完全不生效。

配置本身非法(未知 mode、缺必填字段)同样如此 —— 报错 + 原样转发, 不会静默降级。

日志里搜 `apply param operations` 或 `invalid param_overrides operations`。

---

## 实现

- 代码: `internal/paramops/`(纯函数, 不依赖 gin / DB / settings)
- 接线: `internal/proxy/request_helpers.go` 的 `applyParamOverrides`
- 测试: `internal/paramops/paramops_test.go` + `internal/proxy/request_helpers_test.go`
