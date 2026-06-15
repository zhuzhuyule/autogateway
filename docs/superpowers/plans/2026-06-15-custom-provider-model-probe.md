# Custom 入口 + test_model 重新获取 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** 创建分组流程加「⚙️ 自定义」入口 + test_model「重新获取」(创建时实时探测上游 models,需新后端端点;详情复用 refresh-models)。

**Architecture:** 后端新增 `ProbeUpstreamModels` + `POST /api/groups/probe-models`(临时 base_url+key 拉上游 models);前端加 custom catalog 卡片、创建流程 test_model 刷新按钮(调 probe-models)、详情刷新按钮(复用 refresh-models)。

**Spec:** `docs/superpowers/specs/2026-06-15-custom-provider-model-probe-design.md`

---

## Task 1: 后端 probe-models 端点

**Files:** `internal/services/upstream_probe.go`、`internal/handler/group_handler.go`、`internal/router/router.go`、`internal/services/upstream_probe_test.go`

- [ ] **Step 1: 读现状** Read `upstream_probe.go`(协议→路径映射、base_url 剥离逻辑、httpclient 用法)、`group_handler.go` 的 `RefreshGroupModels`(handler 风格 + 返回 model 列表怎么解析上游响应,可复用其解析)、`router.go:200` 附近 groups 路由组。

- [ ] **Step 2: 写失败测试** `upstream_probe_test.go` 加 `TestProbeUpstreamModels`:用 httptest mock 上游,`/v1/models` 返 `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}` → 断言返回 `["gpt-4o","gpt-4o-mini"]`;`/v1beta/models`(gemini) 返 `{"models":[{"name":"models/gemini-2.5-flash"}]}` → 断言解析出 id;401 → 返错误。

- [ ] **Step 3: 实现 ProbeUpstreamModels**
```go
// ProbeUpstreamModels 用临时 base_url + key 拉上游模型清单。channelType 决定
// 端点路径与响应解析。复用本文件的 base_url 规整逻辑。
func ProbeUpstreamModels(ctx context.Context, baseURL, channelType, key string) ([]string, error)
```
按 channelType 选路径(openai/openai-response → `/v1/models`,gemini → `/v1beta/models`,anthropic → `/v1/models`),带 `Authorization: Bearer {key}` GET,解析:OpenAI 风格取 `data[].id`;gemini 取 `models[].name`(去 `models/` 前缀)。复用现有 base_url 末尾 `/v1` 剥离。

- [ ] **Step 4: handler + 路由** `group_handler.go` 加 `ProbeModels(c *gin.Context)`:bind `{base_url, channel_type, key}` → 调 `ProbeUpstreamModels` → 返 `{models: [...]}`;`router.go` groups 组加 `groups.POST("/probe-models", serverHandler.ProbeModels)`(admin auth,与 refresh-models 同组)。

- [ ] **Step 5: 验证** `go build ./... && go test ./internal/services/ -run TestProbeUpstreamModels -v` 通过。

- [ ] **Step 6: Commit** `git add internal/ && git commit -m "✨ feat(api): probe-models 端点 — 临时 base_url+key 拉上游模型清单"`

---

## Task 2: 前端 Custom catalog 入口

**Files:** `web/src/components/v3/V3NewGroupFlow.vue`、locales

- [ ] **Step 1: 读现状** Read `V3NewGroupFlow.vue` 的 `CatalogItem`(77)、三个 catalog computed(93-109)、catalog 渲染模板(选卡片处)、`ensureGroupExists` 的 custom 分支(280 附近 "走 custom: passthrough")、custom 模式怎么触发/显示 base URL(532 附近)。

- [ ] **Step 2: 加 custom 卡片** `CatalogItem.kind` 加 `"custom"`。构造一个固定 custom 卡片(不依赖 FREE_PROVIDERS,可用一个最小 `FreeProvider` 字面量或单独渲染分支),展示「⚙️ 自定义」+ 说明。放 catalog 显眼处(官方区之后)。选它 → 设置当前为 custom 模式(走已有 passthrough 路径:显示 base_url/channel_type/model 输入,分组名默认 `custom`)。复用已有 custom 分支逻辑,**不改 ensureGroupExists 的 passthrough 转发**。

- [ ] **Step 3: locales** 三语加 custom 卡片文案(`v3.customProviderTitle`/`v3.customProviderDesc` 等),照现有 key 风格。

- [ ] **Step 4: 验证** `cd web && npm run type-check` 通过;人工核对 custom 卡片出现、选它进 custom 模式。

- [ ] **Step 5: Commit** `git add web/src/components/v3/V3NewGroupFlow.vue web/src/locales/ && git commit -m "✨ feat(web): 创建分组加 Custom 自定义入口卡片"`

---

## Task 3: 前端创建流程 test_model 重新获取按钮

**Files:** `web/src/api/*`、`web/src/components/v3/V3NewGroupFlow.vue`、locales

- [ ] **Step 1: api client** 在 `web/src/api/`(看现有 group/key api 文件)加 `probeModels(payload: {base_url, channel_type, key}): Promise<{models: string[]}>` 调 `POST /api/groups/probe-models`。

- [ ] **Step 2: 按钮 + 逻辑** test_model 下拉(`availableModelsForPicked` 渲染处,约 589)旁加「🔄 重新获取」按钮:点击用当前 base_url + channel_type + 已填 key 调 `probeModels`,成功把返回 models 合并进下拉选项(新增 ref `probedModels`,下拉 options = 静态 ∪ probed)。loading 态 + 失败 message;base_url/key 空时禁用。

- [ ] **Step 3: locales** 三语加按钮文案 `v3.refetchModels`/`v3.refetchModelsTip` 等。

- [ ] **Step 4: 验证** `npm run type-check` 通过;人工核对填 base_url+key 点按钮 → 下拉出现真实模型。

- [ ] **Step 5: Commit** `git add web/src/ && git commit -m "✨ feat(web): 创建分组 test_model 一键重新获取(实时探测上游 models)"`

---

## Task 4: 前端详情刷新按钮(复用 refresh-models)

**Files:** `web/src/components/v3/V3GroupDetail.vue`、locales(如需)

- [ ] **Step 1: 确认现状** Read `V3GroupDetail.vue` 看是否已有调 `refresh-models`(`POST /groups/:id/refresh-models`)的刷新按钮(已有"上游模型列表为空,请先刷新"提示)。

- [ ] **Step 2: 补按钮(若缺)** 若无:在模型列表区加「🔄 刷新模型」按钮调 refresh-models api(看 `web/src/api` 有无现成,无则加),成功后重载模型列表 + message。若已有:跳过,在汇报说明。

- [ ] **Step 3: 验证 + Commit** `npm run type-check`;`git add web/src/ && git commit -m "✨ feat(web): 分组详情刷新模型按钮"`(若 Step1 发现已有则本 task 跳过)

---

## Task 5: 收尾

- [ ] `go build ./... && go test ./internal/services/ && cd web && npm run build` 全绿。
- [ ] 对照 spec 验收:custom 卡片可建 passthrough 分组;创建流程 test_model 重新获取出真实模型;详情可刷新;official/free 路径零回归。
- [ ] finishing-a-development-branch(分支 `feat/custom-provider-model-probe`)。

---

## Self-Review
- **Spec coverage**: probe 端点→Task1;custom 入口→Task2;创建 test_model 刷新→Task3;详情刷新→Task4。✅
- **Placeholder**: 后端解析代码给了方向(data[].id / models[].name);前端按现有 catalog/下拉结构现场对齐(行号会漂移)。Task4 条件执行(已有则跳)。
- **Type consistency**: `ProbeUpstreamModels(ctx,baseURL,channelType,key)→([]string,error)`、`probeModels({base_url,channel_type,key})→{models}`、端点 `/api/groups/probe-models` 各处一致。✅
