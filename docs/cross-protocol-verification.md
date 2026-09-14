# 跨协议转译(Anthropic ⇄ OpenAI)验证手册

本文回答一个问题:**这套跨协议转译,怎么验、验到什么程度算通过。**

一键跑:

```bash
bash scripts/verify-cross-protocol.sh
```

跑完输出 `PASS=n FAIL=0` 且退出码 0,即视为通过。下面解释它到底验了什么、
以及不想用脚本时怎么手工验。

---

## 1. 三层验证,别只做一层

| 层 | 验什么 | 需要什么 | 耗时 |
|---|---|---|---|
| **L1 单元 / E2E 测试** | 转换正确性、流式状态机、边界(断流 / gzip / 空流) | 无,纯 `go test` | ~10s |
| **L2 正向实机** | Anthropic 入站 → **真实** OpenAI 上游 | 已配好 `anthropic` 聚合 + alias + 可用 key | ~30s |
| **L3 反向实机** | OpenAI 入站 → Anthropic 上游 | 脚本自建 mock,零外部依赖 | ~30s |

**为什么三层都要**:L1 能覆盖逻辑分支,但覆盖不到"真上游的真实响应形状"和
"HTTP 层细节"。历史上有 3 个 bug 是**只有实机才能暴露**的:

- 非流式转译照抄了上游的 `Content-Length` / `Content-Encoding` → 客户端读到一半断流
- 流式转译漏了 `X-Accel-Buffering: no` → 前置 nginx 把整条 SSE 缓冲到结束才吐
- `message_delta` 漏写 `input_tokens` → 客户端读到的 `input_tokens` 恒为 0

这三个 `httptest` 全都测不出来(测试用的上游不 gzip、也不校验 Content-Length)。
**所以"单测全绿"不等于"能用",L2/L3 不能省。**

---

## 2. 前置条件

### 2.1 改过前端就必须重新构建

前端是 `//go:embed web/dist` **编进二进制**的(`main.go`)。只改 `web/src` 不跑
`npm run build`,你看到的是**旧面板**,会白白怀疑后端。

```bash
cd web && npm run build && cd ..     # 改过 web/src 才需要
```

判定当前产物是否够新:`web/dist` 里应能搜到跨协议相关文案。

```bash
grep -rl "跨协议" web/dist/          # 有输出 = 前端已包含跨协议 UI
```

### 2.2 管理 API 的鉴权 key 在 DB 里,不在 `.env`

`system_settings.auth_key` 才是生效值。拿 `.env` 的 `AUTH_KEY` 会 401。

```bash
AK=$(sqlite3 data/autogateway.db \
  "SELECT setting_value FROM system_settings WHERE setting_key='auth_key' LIMIT 1;")
```

(注意列名是 `setting_value`,不是 `value`。)

### 2.3 本机回环调用要绕开 HTTP_PROXY

如果环境里有 `HTTP_PROXY`,curl 会把 `127.0.0.1` 也丢给代理,表现为莫名其妙的
连接失败。所有本机调用都加 `--noproxy '*'`。

---

## 3. L1:单元 / E2E 测试

```bash
go test ./internal/...            # 全量
go test ./internal/apicompat/ -v  # 只看协议转换
go test ./internal/proxy/ -v      # 只看接线与流式
```

覆盖范围(共 57 个跨协议专项 Test):

| 文件 | Test 数 | 覆盖 |
|---|---|---|
| `internal/apicompat/conversion_test.go` | 20 | 请求/响应双向字段映射、tool_use ↔ tool_calls、图片、thinking |
| `internal/apicompat/normalize_test.go` | 10 | 防御性归一化、tool 配对修复、空参数兜底 |
| `internal/apicompat/stream_test.go` | 14 | 流式状态机、块生命周期、usage 投影、幂等 Finalize |
| `internal/proxy/protocol_bridge_test.go` | 8 | `planTranslation` 路径判定、响应头/压缩处理 |
| `internal/proxy/stream_translate_test.go` | 5 | 流式转译 E2E、断流 / 空流兜底 |

**通过标准**:全部 `ok`,无 FAIL。

---

## 4. L2:正向实机(Anthropic 入站 → OpenAI 上游)

这是最贴近真实用法的方向:客户端说 Anthropic 协议,网关背后只有 OpenAI 节点。

### 4.1 配置(一次性)

1. 建一个 `channel_type=openai` 的普通分组,填上游 + key,跑一次 key 校验。
2. 把它挂进 **`anthropic` 聚合分组**(后台 → 分组 → 聚合 → 添加子分组)。
   跨协议挂载是允许的(`anthropic ↔ openai`),列表里会带 `· 跨协议` 标记。
3. **建 alias,把模型名映射过去 —— 这步不能省**。
   转译解决"结构",不解决"模型名":`claude-*` 在上游并不存在。

   ```
   alias: claude-sonnet-4-5  →  (该子分组, 上游真实模型名)
   ```

   少了这步,聚合的严格路由 `selectNextForModelExcluding` 会**直接 404**
   (不会退化去乱打),表现是清晰的 "model not served"。

### 4.2 手工验证

```bash
AK=$(sqlite3 data/autogateway.db \
  "SELECT setting_value FROM system_settings WHERE setting_key='auth_key' LIMIT 1;")

# 非流式
curl -s --noproxy '*' -X POST http://127.0.0.1:3001/anthropic/v1/messages \
  -H "Authorization: Bearer $AK" -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'

# 流式
curl -s --noproxy '*' -N -X POST http://127.0.0.1:3001/anthropic/v1/messages \
  -H "Authorization: Bearer $AK" -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d '{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

自定义分组走 `/proxy/{group_name}/v1/messages`;系统默认聚合走 `/anthropic/*`。

### 4.3 判定标准

**非流式**必须同时满足:

- HTTP `200`
- 响应体 `type == "message"`,有 `content[]` 与 `stop_reason`
- **不含任何 `choices` 字段**(含了就是没转译)
- 有 `usage.input_tokens` / `usage.output_tokens`

**流式**必须同时满足:

- HTTP `200`
- 响应头 `Content-Type: text/event-stream`、**`X-Accel-Buffering: no`**、`Transfer-Encoding: chunked`
- 事件顺序:`message_start` → `content_block_start` → `content_block_delta`* →
  `content_block_stop` → `message_delta` → `message_stop`
- **`message_delta` 的 `usage.input_tokens` > 0**(历史上这里是 0)
- 多块(如 thinking + text)时,`index` 必须是 0,1,2… 递增且各自成对 start/stop

---

## 5. L3:反向实机(OpenAI 入站 → Anthropic 上游)

方向反过来:客户端说 OpenAI 协议,网关背后是 Anthropic 节点。

> 本仓库当前**没有**配置真实的 Anthropic 协议上游(唯一 `channel_type=anthropic`
> 的是那个聚合分组本身),所以这一层默认由脚本起一个本地 mock 上游来完成。
> 如果你有真实的 Anthropic 兼容上游,把它的分组挂进 `openai` 聚合 + 建 alias,
> 然后把下面第 1 步的 mock 换成它即可。

脚本自动做的事:起 mock Anthropic 上游(:18081)→ 建临时分组 + key → 挂进
`openai` 聚合 → 建 alias → 双向断言 → **清理掉自己造的所有行**。

```bash
bash scripts/verify-cross-protocol.sh          # 跑全套
bash scripts/verify-cross-protocol.sh --keep   # 排障: 保留临时对象不清理
```

### 判定标准

- HTTP `200`,响应体 `object == "chat.completion"`,`choices[0].message.content` 正确
- `finish_reason` 由 Anthropic 的 `end_turn` 正确映射成 `stop`
- usage 投影回 OpenAI 的**包容桶**:`prompt_tokens` = Anthropic `input_tokens`
- **上游侧**收到的必须是 Anthropic 形状 —— 这才是"转译发生了"的直接证据:
  - 路径是 `/v1/messages`
  - 带 `x-api-key` 与 `anthropic-version`
  - `messages[].content` 从字符串变成了 `[{"type":"text","text":...}]` 块数组
- 流式:`data: {"object":"chat.completion.chunk"}` 序列 + 结尾 `data: [DONE]`
- 流式:`stream: true` 已透传给上游

---

## 6. 已知坑(实机踩过的,按顺序)

1. **Slave 冷启动 key 池为空** → 全部 503 `NO_KEYS_AVAILABLE`。
   已在 `88abc48` 修掉(Slave 启动时非破坏性补齐)。若仍遇到,查
   `IS_SLAVE` 与 mesh sync 状态。

2. **空凭据打上游** → 上游返回一个和真实原因无关的 4xx。
   曾在 `4034937` 修掉(残缺 key hash 进轮转)。Gitee 对空 bearer 返回的是
   **400** 而不是 401,极具误导性。定位手法:打出站 `req.Header`。

3. **免费额度 token 调不了付费模型**。Gitee 免费 key 打付费模型会返回
   「当前 API 不支持免费体验」。挑 `free_models` 里的模型挂 alias。

4. **免费额度容易撞 429,而且是「按 key」限的**。连续跑几次验证就会撞到。
   实测同一分组 5 把 key 里 3 把 429、2 把正常 —— 网关会轮转 key,所以通常
   重试一两次就恢复了(实测连打两次都是 200)。要点:
   - **别把 429 当成转译 bug**,先直连上游确认是不是限流。
   - 脚本遇到不可用的候选会自动试下一个(实测一次运行里
     `claude-sonnet-4-5` 429 → `gpt-oss` 404 → 最终落到可用的 alias)。
   - 想稳定验证就给分组多配几把 key,或换付费 token。

5. **alias 指向了非对话模型**(embedding / reranker / tts)会拿到
   400「暂不支持该接口」,看着像转译坏了。验证脚本已自动跳过这类候选;
   手工验时用 `--forward-alias` 显式指定最稳。

6. **`/v1/chat/completions` 不是代理路由**,会落到前端 SPA(返回一段 HTML,
   HTTP 200,很容易误判)。代理路由只有:
   - `/proxy/{group_name}/*path`
   - 系统默认聚合快捷路由:`/openai/*`、`/gemini/*`、`/anthropic/*`

---

## 7. 当前状态(2026-09-14 实测)

```
── L1 单元 / E2E 测试 ──   ✓ go test ./internal/... 全绿
── 启动网关 ──             ✓ 编译通过 / 服务就绪
── L2 正向实机 ──          ✓ 非流式 200 + Anthropic 形状
                          ✓ 流式完整事件序列
                          ✓ message_delta 带真实 input_tokens
                          ✓ X-Accel-Buffering: no
── L3 反向实机 ──          ✓ 非流式 200 + OpenAI 形状
                          ✓ 上游收到 Anthropic 形状请求
                          ✓ 流式 chunk 序列 + [DONE]
                          ✓ stream:true 透传
汇总: PASS=15  FAIL=0  SKIP=0
```

四个象限全部打通:

| 方向 | 非流式 | 流式 |
|---|---|---|
| Anthropic 入 → OpenAI 上游 | ✅ | ✅ |
| OpenAI 入 → Anthropic 上游 | ✅ | ✅ |

**尚未实现**:Gemini 边(设计上预留了 hub 结构,当前只做 OpenAI ⇄ Anthropic)。

---

## 8. 相关文档

- 设计与决策:`docs/superpowers/specs/2026-09-11-cross-protocol-translation-design.md`
- 验证脚本:`scripts/verify-cross-protocol.sh`(`--help` 看全部参数)
