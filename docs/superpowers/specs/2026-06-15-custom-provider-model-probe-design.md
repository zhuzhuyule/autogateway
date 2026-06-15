# 创建分组:Custom 入口 + test_model 重新获取 设计

> 创建分组流程(V3NewGroupFlow)的两项 UX 改进。

## 背景与现状

- **provider catalog 无显式 custom 入口**:`V3NewGroupFlow` 的 catalog 只有 `official`(OpenAI/Anthropic/Gemini)/`recommended`/`free` 三类。用户要接自己的 OpenAI 兼容端点,只能选个官方 provider 再改、或靠粘 key 智能识别——不直观。代码里其实已有 custom passthrough 路径(`ensureGroupExists` 的 custom 分支),只是没有显式入口卡片。
- **test_model 下拉是静态清单**:`availableModelsForPicked` = picked provider 的 `models + paidModels`(freeProviders 写死),不反映上游真实可用模型。
- **探测能力现状**:`ProbeUpstream` 只探协议/连通性,**不返回模型清单**;`POST /groups/:id/refresh-models`(`RefreshGroupModels`)能拉真实 models 但**要求分组已建**。所以"创建时拉真实模型"缺一个能力。

## 目标

1. **Custom 入口**:catalog 加固定的「⚙️ 自定义」卡片,选它走 passthrough,用户自填 base_url/channel_type/model,分组名默认 `custom`。
2. **test_model 重新获取(两条路径)**:
   - 创建时:填了 base_url+key 后点按钮,实时探测上游 `/v1/models` 拉真实模型填充下拉(需新后端端点)。
   - 分组详情:复用已有 `refresh-models`,确认/补前端刷新按钮。

## 非目标(YAGNI)

- 不改 custom passthrough 的既有路由/转发逻辑(只加显式入口)。
- 探测端点不做模型能力(vision/tools)解析,只返 model id 列表。
- 不持久化探测用的 key(仅本次请求用)。

## 设计

### 1. 后端:临时探测 models 端点

新增 `POST /api/groups/probe-models`(admin auth,放在现有 groups 路由组,与 refresh-models 并列):
```go
// 请求
type ProbeModelsRequest struct {
	BaseURL     string `json:"base_url" binding:"required"`
	ChannelType string `json:"channel_type" binding:"required"` // openai | gemini | anthropic | openai-response
	Key         string `json:"key"`                             // 可空(部分 provider /models 不需 key)
}
// 响应
type ProbeModelsResponse struct {
	Models []string `json:"models"`
}
```
逻辑:
- 按 channel_type 选 models 端点路径(复用 `upstream_probe.go` 的协议→路径映射:openai `/v1/models`、gemini `/v1beta/models`、anthropic `/v1/models`)。
- 用 `httpclient` 带 `Authorization: Bearer {key}`(或对应 header)GET 上游,解析返回的 model id 列表(OpenAI 风格 `data[].id`;gemini `models[].name`)。
- base_url 末尾 `/v1` 等的拼接复用 upstream_probe 已有的剥离逻辑(避免 `/v1/v1/models`)。
- 失败(401/超时/解析失败)返回明确错误,前端提示。
- 这是新建 service 函数 `ProbeUpstreamModels(ctx, baseURL, channelType, key) ([]string, error)`(放 `internal/services/upstream_probe.go` 旁)+ handler + 路由。

### 2. 前端:Custom catalog 入口

`V3NewGroupFlow`:
- `CatalogItem.kind` 加 `"custom"`。
- 加一个**固定的 custom 卡片**(不依赖 FREE_PROVIDERS),放 catalog 显眼处(如官方之后/顶部)。卡片展示「⚙️ 自定义」+ 说明"接入任意 OpenAI/Anthropic/Gemini 兼容端点"。
- 选 custom → 进入 custom 模式(复用已有 passthrough 路径):详情区显示 base_url + channel_type + model 输入(本来 custom 就显示 base URL,见现有 `custom 时 base URL` 注释),分组名默认 `custom`(或用户填)。

### 3. 前端:创建流程 test_model 重新获取按钮

- test_model 下拉(`availableModelsForPicked` 渲染处,约 589 行)旁加一个「🔄 重新获取」按钮。
- 点击:用当前表单的 base_url + channel_type + 已填 key 调 `POST /api/groups/probe-models`,成功则把返回 models 合并进 test_model 下拉选项(覆盖或追加静态清单)。
- 按钮 loading 态;失败 message 提示;base_url/key 为空时禁用 + tip。
- API client:`web/src/api/` 加 `probeModels(payload)`。

### 4. 前端:分组详情刷新按钮

- 确认 `V3GroupDetail` 是否已有调 `refresh-models` 的按钮(已有"上游模型列表为空,请先刷新"提示,说明刷新概念存在)。
- 若已有:不动。若缺按钮:在模型列表区加「🔄 刷新模型」按钮调 `POST /groups/:id/refresh-models`。

## 安全

- `probe-models` 端点走 admin auth(与其他 /api/groups 一致)。key 只用于本次上游请求,不落库、不日志明文。

## 测试

- 后端 `ProbeUpstreamModels` 单测:mock 上游 `/v1/models`(OpenAI data[].id)、`/v1beta/models`(gemini models[].name),断言解析出 id 列表;401/超时返错误。
- 前端 type-check 通过;人工核对 custom 卡片渲染、test_model 刷新按钮调用、详情刷新。

## 验收标准

- `go build ./...` + `go test ./internal/services/` + 前端 type-check 通过。
- 创建流程出现「⚙️ 自定义」卡片,选它能填 base_url/model 建 passthrough 分组(名默认 custom)。
- 创建流程填 base_url+key 后点「重新获取」,test_model 下拉出现上游真实模型。
- 分组详情可刷新模型列表(已有则确认可用)。
- 不破坏现有 official/free provider 创建路径(零回归)。

## 影响面

- 后端:`internal/services/upstream_probe.go`(加 ProbeUpstreamModels)、`internal/handler/group_handler.go`(handler)、`internal/router/router.go`(路由)。
- 前端:`web/src/components/v3/V3NewGroupFlow.vue`(custom 卡片 + test_model 刷新按钮)、`web/src/api/*`(probeModels)、`web/src/components/v3/V3GroupDetail.vue`(刷新按钮,如缺)、locales(按钮文案)。
- 无 DB schema 变更。
