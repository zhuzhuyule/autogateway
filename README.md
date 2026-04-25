# AutoGateway

> **一站式 AI API 网关** —— 把分散的免费/付费模型接入统一为一个 Key、一组路径,客户端代码零改动。

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)]()
[![Vue](https://img.shields.io/badge/Vue-3-4FC08D?logo=vuedotjs)]()
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker)]()

---

## 这是什么

AutoGateway 是自托管的 AI API 网关。它在 [GPT-Load](https://github.com/tbphp/gpt-load) 的密钥池/负载均衡基础上,做了三件主要的事:

1. **聚合**:把 Groq / Cerebras / OpenRouter / Gemini / Anthropic 等十多家分散的 Key 池统一为一个对外入口
2. **路由**:按请求复杂度(token / tools / vision)自动选择档位,免费档够用就不烧钱
3. **简化**:一个 `AUTH_KEY` + 一组无前缀路径(`/openai/v1/...`、`/anthropic/v1/...`、`/gemini/v1beta/...`),客户端继续用原版 SDK

```
你的 App ──┐
你的 Cursor ┤── 一个 base_url + 一个 AUTH_KEY ──→ AutoGateway ──┬─→ Groq        (免费)
你的 Cline ─┤                                            ├─→ Cerebras    (免费)
脚本 / CI ──┘                                            ├─→ OpenRouter  (含免费档)
                                                          ├─→ OpenAI      (付费)
                                                          └─→ Anthropic   (付费)

           按复杂度路由 / 故障切换 / 加权负载 / 模型重定向
```

## 核心特性

### 🔌 免费 Provider 一键接入
内置 10 家主流免费 LLM 提供商(Groq、Cerebras、OpenRouter、Together、Cloudflare Workers AI、Mistral、Google AI Studio、Cohere、GitHub Models、Hugging Face Router),创建分组时点击即可一键预填 base_url、channel_type、test_model。你只需要粘贴自己注册到的 API Key。

清单维护在 [`web/src/data/freeProviders.ts`](web/src/data/freeProviders.ts),欢迎 PR 补充。

### 🧩 系统默认聚合(永久存在,不可删除)
启动时自动建好三个聚合分组,按 channel 永久接管:

| 系统聚合 | 快捷路径 | 接管的子分组 |
|---|---|---|
| `default-openai`    | `POST /openai/v1/chat/completions` | 所有 `channel_type=openai` 的分组 |
| `default-gemini`    | `POST /gemini/v1beta/...`           | 所有 `channel_type=gemini` 的分组 |
| `default-anthropic` | `POST /anthropic/v1/messages`       | 所有 `channel_type=anthropic` 的分组 |

新建/已有的标准分组**自动挂入**对应聚合,无需手动配置子分组关系。三者**共享同一个 AUTH_KEY**,真正"一个 Key 调所有"。

### ⚡ 智能复杂度路由 (Auto Routing)

```
简单 (tokens < 2000, 无 tools)    → simple_group   省钱档
中等 (2000-8000 或 tools 1-3)     → medium_group   平衡档
复杂 (>8000 / vision / tools > 3) → complex_group  旗舰档
```

管理面板提供:
- **三档预设**(省钱/平衡/性能)一键应用
- **路由测试器** 粘贴请求 JSON,实时看到分类等级、估算 tokens、命中分组
- **分组下拉带模型计数**,例如 `Groq Cloud (groq) · 18 个模型`
- **每行映射下方显示样例**:`simple: gpt-4o-mini, llama-3.1-8b...  +5`,配置时不用切页找

### 🔍 上游真实模型一键拉取 + 浏览
- 一键调用上游 `/v1/models` (OpenAI / Anthropic / Gemini 三种协议自动识别),把真实可用模型缓存到分组
- **`test_model` 字段从输入框升级为下拉选项**(可搜索 + 自由手填兜底),不用再手输全名
- **多处可视化**:
  - **创建/编辑分组弹窗**:折叠面板"上游可用模型",可搜索/复制/"设为测试模型"/即时刷新
  - **分组详情(只读)**:无需点编辑,选中分组即可浏览
  - **聚合分组详情**:展示**子分组合并去重后**的总模型数,每条显示由哪些子分组提供——一眼看出"我这个聚合一共能调多少模型 / 哪些模型有冗余"
  - **Model Catalog 全局页**:跨分组聚合的总模型,带筛选(搜索/档位/Provider/仅免费)
- **🆓 免费徽标 + 能力档位标签**(Fast / Balanced / Max),内置 30+ 条已知免费模型清单,免费优先排序

### 🔄 自动模型去重
跨分组扫描相同模型(比如多家都提供 `llama-3.3-70b`),给出聚合建议;`GET /openai/v1/models` 暴露统一目录。

### 🔐 多档隔离
每个分组可以单独设 `proxy_keys`,团队/场景间互不干扰;系统默认聚合的 key 与管理 key 共享,简化常见场景。

---

## 快速开始

需要 **Docker + Docker Compose**。

```bash
git clone https://github.com/<your-org>/autogateway.git
cd autogateway

# 1. 设一个强密码作为 AUTH_KEY (这就是后续所有 API 调用的 Key)
cat > .env <<EOF
AUTH_KEY=sk-prod-$(openssl rand -hex 16)
PORT=3001
EOF

# 2. 启动
docker compose up --build -d

# 3. 打开管理面板,用 .env 里的 AUTH_KEY 登录
open http://localhost:3001
```

启动后系统会自动建好三个默认聚合分组,日志可看到:
```
system aggregate group default-openai created
system aggregate group default-gemini created
system aggregate group default-anthropic created
```

### 添加你的第一个免费 Provider

1. 管理面板 → **Keys** → "新建分组"
2. 顶部 "**从免费 Provider 快速开始**" 折叠面板里点 **Groq Cloud**(其他字段会自动填好)
3. 顺手在卡片上点"去注册"拿一个免费 Key
4. 保存分组 → 该分组**自动加入** `default-openai`
5. 在分组的 Keys 列表里粘贴你的 Key

完成。现在可以这样调用:

```bash
curl http://localhost:3001/openai/v1/chat/completions \
  -H "Authorization: Bearer $YOUR_AUTH_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "llama-3.3-70b-versatile",
    "messages": [{"role":"user","content":"Hello"}]
  }'
```

OpenAI SDK 用法(只改 base_url):

```python
from openai import OpenAI
client = OpenAI(
    base_url="http://localhost:3001/openai/v1",
    api_key="<YOUR_AUTH_KEY>",
)
client.chat.completions.create(
    model="llama-3.3-70b-versatile",
    messages=[{"role": "user", "content": "Hello"}],
)
```

---

## API 端点速查

### 系统默认(快捷路径)
| 端点 | 说明 |
|---|---|
| `POST /openai/v1/chat/completions`   | OpenAI 兼容,自动从所有 OpenAI channel 子分组里挑 |
| `GET  /openai/v1/models`             | 统一模型目录 |
| `POST /anthropic/v1/messages`        | Anthropic 原生协议 |
| `POST /gemini/v1beta/models/{model}:generateContent` | Gemini 原生协议 |

所有端点用 `Authorization: Bearer <AUTH_KEY>`。

### 自定义命名分组
```
POST /proxy/{group_name}/v1/chat/completions
```
每个分组可以单独设 `proxy_keys` 做团队隔离。

### 管理 API(常用)
| 端点 | 说明 |
|---|---|
| `GET    /api/groups` / `/api/groups/list` | 分组列表(含 `available_models` 缓存 + `models_refreshed_at`) |
| `POST   /api/groups` / `PUT /api/groups/{id}` | 增删改分组 |
| `POST   /api/groups/{id}/refresh-models` | **拉取上游真实模型列表**(自动按 channel_type 选择 endpoint) |
| `POST   /api/groups/{id}/keys` 等 | Key 管理 |
| `GET    /api/models` | 跨分组聚合的统一模型目录 |
| `GET    /api/auto-routing/config` / `POST /api/auto-routing/config` | Auto Routing 配置 |
| `POST   /api/auto-routing/test` | 路由测试器(粘贴请求 JSON,返回分类与目标分组) |
| `GET    /api/dedup/suggestions` / `POST /api/dedup/create` | 模型去重建议 |

---

## 内置免费 Provider 清单

| 厂商 | 免费档 | 亮点 |
|---|---|---|
| Groq Cloud           | 14400 req/day      | LPU 极速推理 |
| Cerebras             | 限速但无每日上限    | 晶圆级 70B |
| OpenRouter           | `:free` 模型免费    | 300+ 模型聚合 |
| Together AI          | $1 试用 + 免费档    | DeepSeek / Llama 系列 |
| Cloudflare Workers AI| 10000 neuron/day   | 边缘节点推理 |
| Mistral La Plateforme| Experimental tier  | Codestral / Large |
| Google AI Studio     | 高免费额度          | Gemini 2.0 / 2.5 Flash |
| Cohere               | Trial Key          | Command R+ |
| GitHub Models        | GitHub 账号免费     | 多家旗舰模型 |
| Hugging Face Router  | 限速免费            | 多 provider 聚合 |

---

## 配置(常用环境变量)

| 变量 | 默认 | 说明 |
|---|---|---|
| `AUTH_KEY` | — | **必填**,管理面板登录密钥 + 默认聚合 proxy_keys |
| `PORT` | 3001 | HTTP 端口 |
| `DATABASE_DSN` | `./data/autogateway.db` | SQLite / MySQL / PostgreSQL DSN |
| `REDIS_DSN` | (空) | 留空走内存存储 |
| `SERVER_GRACEFUL_SHUTDOWN_TIMEOUT` | 10 | 优雅关停超时(秒) |

完整变量参见 [GPT-Load 文档](https://github.com/tbphp/gpt-load#environment-variables)(本项目继承其底座配置)。

---

## 架构

```
                +------------- Web UI (Vue 3 + Naive UI) ----------+
                |  Auto Routing  |  Free Providers  |  Catalog     |
                +-------------------------+-------------------------+
                                          |
                +-------------------------v-------------------------+
                |  HTTP Router (Gin)                                |
                |  /api/*   /proxy/{name}/*   /openai/*  ...        |
                +-------------------------+-------------------------+
                                          |
+---------------+  +-----------+  +-------v---------+  +----------+
| Auto Route    |  | Aggregate |  | Channel Factory |  | Key Pool |
| (complexity   |  | (sub-grp  |  | openai / gemini |  | (round-  |
|  classifier)  |  |  weights) |  |  / anthropic)   |  |  robin)  |
+---------------+  +-----------+  +-----------------+  +----------+
                                          |
                                          v
                上游: Groq, OpenRouter, OpenAI, Anthropic, ...
```

技术栈:Go 1.24 (Gin / GORM) + Vue 3 (Naive UI / Pinia) + SQLite/MySQL/PostgreSQL/Redis 任选。

---

## Roadmap

- [x] 系统默认聚合(default-openai/gemini/anthropic)+ 快捷路径
- [x] 免费 Provider 一键预填 + 已添加状态徽标
- [x] 智能复杂度路由 + 三档预设 + 路由测试器
- [x] 上游 `/v1/models` 实时拉取 + 多处模型浏览
- [x] 免费 / 能力档位(Fast/Balanced/Max)模型徽标
- [ ] 跨 channel 协议翻译(OpenAI ↔ Anthropic ↔ Gemini)
- [ ] 用量配额、预算告警、按 Key 计费
- [ ] OAuth / SSO 登录
- [ ] 子分组健康度自动权重调整
- [ ] OpenAI Realtime / Audio API 透传
- [ ] ASR / TTS / Embeddings 多模态网关
- [ ] MCP server 集成

---

## 致谢

- [GPT-Load](https://github.com/tbphp/gpt-load) — 提供本项目的密钥池与代理底座
- [awesome-free-llm-apis](https://github.com/mnfst/awesome-free-llm-apis) — 免费 Provider 清单参考
- [Naive UI](https://www.naiveui.com/) — Vue 组件库

## License

MIT — 见 [LICENSE](LICENSE)
