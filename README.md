# AutoGateway

> **一站式 AI API 网关** —— 把分散的免费/付费模型接入统一为一个 Key、一组路径,客户端代码零改动。

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)]()
[![Vue](https://img.shields.io/badge/Vue-3-4FC08D?logo=vuedotjs)]()
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker)]()

---

## 这是什么

AutoGateway 是自托管的 AI API 网关。它在 [GPT-Load](https://github.com/tbphp/gpt-load) 的密钥池/负载均衡底座上,做了三件主要的事:

1. **聚合** — 把 Groq / Cerebras / OpenRouter / Gemini / Anthropic 等十多家分散的 Key 池,统一为一个对外入口
2. **路由** — 聚合分组按"哪个子分组能 serve 这个模型"精准转发,再叠加按请求复杂度自动选档,免费档够用就不烧钱
3. **简化** — 一个 `AUTH_KEY` + 一组无前缀路径(`/openai/v1/...`、`/anthropic/v1/...`、`/gemini/v1beta/...`),客户端继续用原版 SDK

```
你的 App ──┐
你的 Cursor ┤── 一个 base_url + 一个 AUTH_KEY ──→ AutoGateway ──┬─→ Groq        (免费)
你的 Cline ─┤                                            ├─→ Cerebras    (免费)
脚本 / CI ──┘                                            ├─→ OpenRouter  (含免费档)
                                                          ├─→ OpenAI      (付费)
                                                          └─→ Anthropic   (付费)

    模型感知路由 / 故障切换 / 加权负载 / 别名重定向 / 多端同步
```

---

## 核心特性

### 🔌 免费 Provider 一键接入
内置 10 家主流免费 LLM 提供商(Groq、Cerebras、OpenRouter、Together、Cloudflare Workers AI、Mistral、Google AI Studio、Cohere、GitHub Models、Hugging Face Router)。创建分组时点一下即可自动预填 `base_url` / `channel_type` / `test_model`,你只需粘贴自己注册到的 API Key。清单维护在 [`web/src/data/freeProviders.ts`](web/src/data/freeProviders.ts),欢迎 PR 补充。

### 🧩 系统默认聚合(永久存在,不可删除)
启动时自动建好三个聚合分组,按 channel 永久接管:

| 系统聚合 | 快捷路径 | 接管的子分组 |
|---|---|---|
| `openai`    | `POST /openai/v1/chat/completions` | 所有 `channel_type=openai` 的分组 |
| `gemini`    | `POST /gemini/v1beta/...`          | 所有 `channel_type=gemini` 的分组 |
| `anthropic` | `POST /anthropic/v1/messages`      | 所有 `channel_type=anthropic` 的分组 |

新建/已有的标准分组**自动挂入**对应聚合,无需手动配置子分组关系。三者**共享同一个 AUTH_KEY** 作为 proxy_key,真正"一个 Key 调所有"。

### 🧭 路由模式:聚合严格路由 + 子分组透传/白名单
路由分**两层**,语义清晰:

**聚合层(aggregate group)—— 严格路由,不透传**
请求里的 `model` 必须落在某个子分组的"已知模型集合"里(各子分组 `exposed_models ∪ available_models` 的并集),命中者之间按**加权 SWRR** 选择并转发;**没有任何子分组声明能 serve 这个模型 → 直接 404**,不会碰运气打上游。聚合层本身**不做 passthrough**,透传只是子分组自身的属性。

叠加**智能候选路由(P4)**:聚合分组上先按 `alias > family > raw` 解析候选池,命中则钉死子分组 + 把 `body.model` 改写成上游真实名,失败自动 fallback 到候选池里的下一个子分组。

**子分组层(standard group)—— 由 `model_routing_mode` 决定**

| 模式 | 行为 |
|---|---|
| `passthrough`(默认) | 上游声明的全部模型直通,用 `available_models` |
| `specified`         | 只允许 `exposed_models` 白名单里的模型,否则返回 405 |

### 📡 请求内容如何透传
match 到子分组后,转发给上游 provider 的内容:

- **body — 基本全量透传**:你发的 `messages` / `temperature` / `tools` / `stream` 等原样转发。仅三种情况会动:① `model` 字段在命中 alias/redirect/candidate 时改写成上游真名;② 该分组配了 `param_overrides` 时注入/覆盖对应参数;③ 其余一律原样。
- **header — 全量克隆,但认证凭证一定被换**:先把你的所有 header 克隆过去,再删掉 `Authorization` / `X-Api-Key` / `X-Goog-Api-Key`,换成该子分组自己的 provider key(你的客户端 Key 绝不流到上游);若配了 header rules 再按规则增删改。

### ⚡ 智能复杂度路由 (Auto Routing)
```
简单 (tokens < 2000, 无 tools)    → simple_group   省钱档
中等 (2000-8000 或 tools 1-3)     → medium_group   平衡档
复杂 (>8000 / vision / tools > 3) → complex_group  旗舰档
```
管理面板提供**三档预设**(省钱/平衡/性能)一键应用、**路由测试器**(粘贴请求 JSON 实时看分类等级/估算 tokens/命中分组)、分组下拉带模型计数、每行映射下方显示样例模型。

### 📚 聚合模型目录:全子分组并集 + 12h 后台刷新
聚合分组的 `GET /v1/models` 返回**所有子分组"可调模型"的并集**(specified 子分组取 `exposed_models` 白名单,passthrough 取 `available_models`),去重后附加全局别名。**纯读缓存、稳定完整** —— 每次请求返回一致,不会像单纯转发那样随机命中某个子分组而漂移。

`available_models` 缓存由**后台定时服务每 12 小时刷新**:并发拉取各 standard 分组上游 `/v1/models`,单个失败仅告警、保留旧缓存,刷新后自动重建聚合路由集合。也可在管理面板对某分组点"刷新模型"即时补拉。

### 🔍 上游真实模型拉取 + 多处浏览
- 一键调用上游 `/v1/models`(OpenAI / Anthropic / Gemini 三协议自动识别),把真实可用模型缓存到分组
- `test_model` 从输入框升级为**可搜索下拉**(自由手填兜底)
- **创建/编辑弹窗**、**分组详情(只读)**、**聚合详情(子分组合并去重总数 + 每条由哪些子分组提供)**、**Model Catalog 全局页**(跨分组聚合 + 搜索/档位/Provider/仅免费筛选)多处可视化
- **🆓 免费徽标 + 能力档位标签**(Fast / Balanced / Max),内置 30+ 条已知免费模型清单,免费优先排序

### 🔁 模型去重 + 别名
- 跨分组扫描相同模型(比如多家都提供 `llama-3.3-70b`)给出聚合建议
- **别名(alias)**:给上游真实模型起对外可调的虚拟名,`model_aliases` 支持逐条启用;别名与真实模型是 N:N 关系,同步时一并传播

### 🛰️ 多端同步 (mesh,支持穿透)
多台实例通过 **WebSocket 双向 mesh** 同步配置/分组/密钥/别名/系统设置:
- **LWW 逐记录合并**:按 `max(updated_at, deleted_at)` 判定胜者,字段级删除也能正确传播,软删除 tombstone 不被对端还原
- **穿透模式**:没有公网 IP 的机器(家里的 Mac mini 等)可主动连到有公网的节点(VPS)完成双向同步
- **`proxy_url` 逐机器不同步**(每台可用各自的出口代理),其余配置全网一致

### 🎬 视频异步任务队列
上游的视频生成(如 `/v1/videos`)常是**同步阻塞数分钟**的长请求,容易超时。AutoGateway 用后台 worker 持有那个阻塞请求,前端**入队 + 轮询**,聊天窗口感知"已等待时长"并在完成后回填结果。任务级租约保证多实例部署下同一任务不被重复执行,某实例崩溃后其过期任务可被别的节点接管。

### 🔑 密钥池:轮询 / 失效检测 / 冷却分级
- 每个分组多 Key **加权轮询**,请求失败自动标记失效
- **CronChecker** 定时重验失效 Key,恢复即回池
- **冷却分级 + 速率账本(rpm/rpd)+ 粘性会话 + 采样路由**:429/超限的 Key 按严重度冷却,流式请求保持 turn 完整性
- 密钥列表按 id 稳定排序(测试/新增不跳动),编辑弹窗全局预填原 Key

### 🔐 多档隔离
每个分组可单独设 `proxy_keys` 做团队/场景隔离;系统默认聚合的 key 与管理 key 共享,简化常见场景。

### 🛰️ Mission Console 管理面板 (v3 UI)
深色顶栏实时遥测(Live keys / RPM / Error rate / Endpoint),网格底纹 + 信号色(ok/warn/danger),Geist Sans + Geist Mono 字族,完整暗色主题。覆盖 Dashboard / Groups & Keys / Auto Routing / Model Catalog / Logs / Settings / Playground / Login 全视图。

---

## 快速开始

需要 **Docker + Docker Compose**。

```bash
git clone https://github.com/zhuzhuyule/autogateway.git
cd autogateway

# 1. 设一个强密码作为 AUTH_KEY (这就是后续所有 API 调用 + 管理面板登录的 Key)
cat > .env <<EOF
AUTH_KEY=sk-prod-$(openssl rand -hex 16)
PORT=3001
EOF

# 2. 启动 (镜像已预 build,multi-arch amd64+arm64)
docker compose up -d

# 3. 打开管理面板,用 .env 里的 AUTH_KEY 登录
open http://localhost:3001
```

启动后自动建好三个默认聚合分组,日志可见 `system aggregate group openai created` 等。

> 想始终用最新版:compose 的 image 已是 `:latest`,升级只需 `docker compose pull && docker compose up -d`。

### 添加你的第一个免费 Provider
1. 管理面板 → **Keys** → "新建分组"
2. 顶部"**从免费 Provider 快速开始**"里点 **Groq Cloud**(其他字段自动填好)
3. 点"去注册"拿一个免费 Key
4. 保存分组 → 该分组**自动加入** `openai` 聚合
5. 在分组 Keys 列表里粘贴你的 Key

完成。现在可以这样调用:

```bash
curl http://localhost:3001/openai/v1/chat/completions \
  -H "Authorization: Bearer $YOUR_AUTH_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"llama-3.3-70b-versatile","messages":[{"role":"user","content":"Hello"}]}'
```

OpenAI SDK 用法(只改 base_url):

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:3001/openai/v1", api_key="<YOUR_AUTH_KEY>")
client.chat.completions.create(
    model="llama-3.3-70b-versatile",
    messages=[{"role": "user", "content": "Hello"}],
)
```

---

## 忘记登录密钥?一键重置(不用碰数据库)

`auth_key` 首次从环境变量 `AUTH_KEY` 写入数据库,之后以**数据库里的值为准**(可在管理面板改,即时生效)。所以忘了密钥时,直接改 `AUTH_KEY` 是不生效的 —— 用 **`RESET_AUTH_KEY`**:

```bash
# 在 .env 里加一行,然后重启一次
RESET_AUTH_KEY=sk-你的新密钥
docker compose up -d          # 启动时把 auth_key(和聚合 proxy_key)重置为该值,用它登录即可
```

**一次性、用完自动失效**:数据库记录该值的指纹(sha256),**同一个值只会生效一次**。所以这行可以一直留在 `.env` 里,不会每次重启都覆盖你在 UI 改过的密钥;想再次重置时,把它**改成新值**再重启即可。

---

## API 端点速查

### 系统默认(快捷路径)
| 端点 | 说明 |
|---|---|
| `POST /openai/v1/chat/completions`   | OpenAI 兼容,自动从所有 OpenAI channel 子分组里模型感知路由 |
| `GET  /openai/v1/models`             | 该聚合下**所有子分组可调模型的并集** + 别名 |
| `POST /anthropic/v1/messages`        | Anthropic 原生协议 |
| `POST /gemini/v1beta/models/{model}:generateContent` | Gemini 原生协议 |

所有代理端点用 `Authorization: Bearer <AUTH_KEY>`(或该分组的 `proxy_keys`)。

### 自定义命名分组
```
POST /proxy/{group_name}/v1/chat/completions
GET  /proxy/{group_name}/v1/models
```

### 管理 API(常用)
| 端点 | 说明 |
|---|---|
| `GET  /api/groups` / `/api/groups/list` | 分组列表(含 `available_models` 缓存 + `models_refreshed_at`) |
| `POST /api/groups` / `PUT /api/groups/{id}` | 增删改分组 |
| `POST /api/groups/{id}/refresh-models` | 拉取上游真实模型列表(按 channel_type 自动选 endpoint) |
| `POST /api/keys/*` | Key 管理(add / test-multiple / delete / restore / clear-invalid ...) |
| `GET  /api/models` | 跨分组聚合的统一模型目录(全局 Catalog) |
| `GET  /api/auto-routing/config` / `POST /api/auto-routing/config` | Auto Routing 配置 |
| `POST /api/auto-routing/test` | 路由测试器(粘贴请求 JSON,返回分类与目标分组) |
| `GET  /api/aliases/quick/models` / `POST /api/aliases/quick/create` | 别名快速整理(家族候选 + 原子批量创建) |

---

## 配置(环境变量)

`.env` 由 `docker-compose.yml` 的 `env_file` 读入。

| 变量 | 默认 | 说明 |
|---|---|---|
| `AUTH_KEY` | — | **必填**,管理面板登录密钥 + 默认聚合 proxy_key。首次写入 DB 后以 DB 为准 |
| `RESET_AUTH_KEY` | (空) | 一次性重置 `auth_key`(见上文),同值只生效一次,可长留 |
| `PORT` | 3001 | HTTP 端口 |
| `HOST` | 0.0.0.0 | 监听地址 |
| `ENCRYPTION_KEY` | (空) | 数据库中 Key 的加密密钥,留空则明文存储 |
| `REDIS_DSN` | (空) | 留空走内存存储;多实例共享状态时填 Redis |
| `IS_SLAVE` | false | 标记为从节点(mesh 同步中不作为写入主源) |
| `MAX_CONCURRENT_REQUESTS` | — | 全局最大并发请求数 |
| `SERVER_READ_TIMEOUT` / `SERVER_WRITE_TIMEOUT` / `SERVER_IDLE_TIMEOUT` | — | HTTP 服务超时(秒) |
| `SERVER_GRACEFUL_SHUTDOWN_TIMEOUT` | 10 | 优雅关停超时(秒) |
| `ENABLE_CORS` / `ALLOWED_ORIGINS` / `ALLOWED_METHODS` / `ALLOWED_HEADERS` / `ALLOW_CREDENTIALS` | — | CORS 控制 |
| `LOG_ENABLE_FILE` | false | 是否额外写文件日志 |

> 数据库路径、连接池等底座级配置继承自 [GPT-Load 文档](https://github.com/tbphp/gpt-load#environment-variables)。大多数运行期配置(超时/限流/日志保留等)也可在**管理面板 → 系统设置**里改,即时生效并随 mesh 同步。

---

## 多端同步与部署

- **镜像**:`ghcr.io/zhuzhuyule/autogateway`(multi-arch: `linux/amd64` + `linux/arm64`),tag 无 `v` 前缀(如 `2.5.21`),`:latest` 跟随最新
- **数据持久化**:`./data:/app/data`(SQLite 库 + 免费模型缓存),升级不丢数据
- **多机同步**:在有公网 IP 的节点与内网节点间通过 WebSocket 建立 mesh,配置/分组/密钥/别名/设置自动 LWW 合并;`proxy_url` 逐机器保留
- **升级**:`docker compose pull && docker compose up -d`

---

## 内置免费 Provider 清单

| 厂商 | 免费档 | 亮点 |
|---|---|---|
| Groq Cloud           | 14400 req/day     | LPU 极速推理 |
| Cerebras             | 限速但无每日上限   | 晶圆级 70B |
| OpenRouter           | `:free` 模型免费   | 300+ 模型聚合 |
| Together AI          | $1 试用 + 免费档   | DeepSeek / Llama 系列 |
| Cloudflare Workers AI| 10000 neuron/day  | 边缘节点推理 |
| Mistral La Plateforme| Experimental tier | Codestral / Large |
| Google AI Studio     | 高免费额度         | Gemini 2.0 / 2.5 Flash |
| Cohere               | Trial Key         | Command R+ |
| GitHub Models        | GitHub 账号免费    | 多家旗舰模型 |
| Hugging Face Router  | 限速免费           | 多 provider 聚合 |

---

## 架构

```
                +------------- Web UI (Vue 3 + Naive UI) ----------+
                |  Dashboard | Auto Routing | Catalog | Playground |
                +-------------------------+-------------------------+
                                          |
                +-------------------------v-------------------------+
                |  HTTP Router (Gin)                                |
                |  /api/*   /proxy/{name}/*   /openai/*  ...        |
                +-------------------------+-------------------------+
                                          |
   +-----------+ +-----------+ +----------v-----+ +---------+ +-----------+
   | Auto      | | Aggregate | | Channel        | | Key     | | Mesh Sync |
   | Route     | | (SWRR +   | | Factory        | | Pool    | | (WS/LWW)  |
   | (classify)| |  model    | | openai/gemini/ | | (rotate/| |  多端同步 |
   |           | |  aware)   | | anthropic)     | |  cool)  | |           |
   +-----------+ +-----------+ +----------------+ +---------+ +-----------+
                                          |
                                          v
                上游: Groq, OpenRouter, OpenAI, Anthropic, ...
```

**技术栈**:Go 1.25 (Gin / GORM / dig DI) + Vue 3 (Naive UI + Geist 字族 + 自定义 Mission Console 设计层) + SQLite/MySQL/PostgreSQL/Redis 任选。后台服务:CronChecker(Key 验证)、模型刷新(12h)、日志清理、视频任务 worker、mesh 同步。

---

## Roadmap

- [x] 系统默认聚合(openai/gemini/anthropic)+ 快捷路径 + 标准分组自动挂入
- [x] 聚合严格路由 + 智能候选路由(alias/family/raw)+ 故障 fallback
- [x] 免费 Provider 一键预填 + 已添加状态徽标
- [x] 智能复杂度路由 + 三档预设 + 路由测试器
- [x] 上游 `/v1/models` 实时拉取 + 多处模型浏览 + 免费/能力档位徽标
- [x] 聚合 `/v1/models` 返回全子分组并集 + 后台 12h 刷新
- [x] 多端 mesh 同步(LWW / 穿透模式 / 字段级删除)
- [x] 视频异步任务队列(后台 worker + 任务级租约)
- [x] `RESET_AUTH_KEY` 一键重置登录密钥(指纹幂等)
- [x] Mission Console v3 UI + Playground
- [ ] New Group 三步流程(pick provider → get key → paste & test)
- [ ] 跨 channel 协议翻译(OpenAI ↔ Anthropic ↔ Gemini)
- [ ] 用量配额、预算告警、按 Key 计费
- [ ] OAuth / SSO 登录
- [ ] 子分组健康度自动权重调整
- [ ] ASR / TTS / Embeddings 多模态网关 · MCP server 集成

---

## 致谢

- [GPT-Load](https://github.com/tbphp/gpt-load) — 本项目的密钥池与代理底座
- [awesome-free-llm-apis](https://github.com/mnfst/awesome-free-llm-apis) — 免费 Provider 清单参考
- [Naive UI](https://www.naiveui.com/) — Vue 组件库
- [Geist](https://vercel.com/font) — Mission Console 视觉基底字族

## License

MIT — 见 [LICENSE](LICENSE)
