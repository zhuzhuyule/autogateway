#!/usr/bin/env bash
# 跨协议转译(Anthropic ⇄ OpenAI)端到端验证脚本
#
#   bash scripts/verify-cross-protocol.sh              # 全量: 单测 + 正向 + 反向
#   bash scripts/verify-cross-protocol.sh --unit-only  # 只跑单测(最快)
#   bash scripts/verify-cross-protocol.sh --no-build   # 复用已有二进制
#   bash scripts/verify-cross-protocol.sh --keep       # 不清理临时对象(排障用)
#   bash scripts/verify-cross-protocol.sh --forward-alias claude-sonnet-4-5
#                                                      # 显式指定正向验证用哪个 alias
#                                                      # (默认自动挑, 跳过 embed/rerank/tts 等)
#
# 分层:
#   L1 单元/E2E 测试  go test —— 覆盖双向 × 流式/非流式 × 边界(断流/gzip/空流)
#   L2 正向实机验证   Anthropic 入站 → 真实 OpenAI 上游(需已配好 anthropic 聚合)
#   L3 反向实机验证   OpenAI 入站 → 本地 mock Anthropic 上游(脚本自建自清)
#
# 退出码: 0 = 全通过; 1 = 有 FAIL。

set -uo pipefail

PORT="${PORT:-3001}"
MOCK_PORT="${MOCK_PORT:-18081}"
KEEP=0
NO_BUILD=0
UNIT_ONLY=0
FORWARD_ALIAS_OPT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) PORT="$2"; shift 2 ;;
    --mock-port) MOCK_PORT="$2"; shift 2 ;;
    --forward-alias) FORWARD_ALIAS_OPT="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --no-build) NO_BUILD=1; shift ;;
    --unit-only) UNIT_ONLY=1; shift ;;
    -h|--help) awk 'NR>1 && /^#/ {sub(/^# ?/,""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# 本机回环调用必须绕开 HTTP_PROXY, 否则 curl 会把 127.0.0.1 也丢给代理
NOPROXY=(--noproxy '*')

BIN="${TMPDIR:-/tmp}/autogateway-verify"
MOCK_PY="${TMPDIR:-/tmp}/mock_anthropic_upstream_$$.py"
SERVER_LOG="${TMPDIR:-/tmp}/autogateway-verify.log"
MOCK_LOG="${TMPDIR:-/tmp}/mock-anthropic-verify.log"

PASS=0
FAIL=0
SKIP=0
SERVER_PID=""
MOCK_PID=""
TMP_GROUP_ID=""
TMP_ALIAS_ID=""
TMP_SUB_MOUNTED=0

c_ok()   { printf '\033[32m%s\033[0m\n' "$1"; }
c_bad()  { printf '\033[31m%s\033[0m\n' "$1"; }
c_warn() { printf '\033[33m%s\033[0m\n' "$1"; }
c_dim()  { printf '\033[2m%s\033[0m\n' "$1"; }

pass() { PASS=$((PASS+1)); c_ok   "  ✓ $1"; }
fail() { FAIL=$((FAIL+1)); c_bad  "  ✗ $1"; }
skip() { SKIP=$((SKIP+1)); c_warn "  - $1"; }
head1() { echo; echo "── $1 ────────────────────────────────────────"; }

cleanup() {
  [[ "$KEEP" == "1" ]] && { c_warn "  --keep: 保留临时对象 (group=$TMP_GROUP_ID alias=$TMP_ALIAS_ID, mock=$MOCK_PID, server=$SERVER_PID)"; return; }

  # 1) 走 API 软删(用应用自己的语义)
  if [[ -n "${AK:-}" ]]; then
    [[ -n "$TMP_ALIAS_ID" ]] && curl -s "${NOPROXY[@]}" -X DELETE \
      "http://127.0.0.1:$PORT/api/aliases/$TMP_ALIAS_ID" -H "Authorization: Bearer $AK" >/dev/null 2>&1
    [[ "$TMP_SUB_MOUNTED" == "1" && -n "$TMP_GROUP_ID" ]] && curl -s "${NOPROXY[@]}" -X DELETE \
      "http://127.0.0.1:$PORT/api/groups/1/sub-groups/$TMP_GROUP_ID" -H "Authorization: Bearer $AK" >/dev/null 2>&1
    [[ -n "$TMP_GROUP_ID" ]] && curl -s "${NOPROXY[@]}" -X DELETE \
      "http://127.0.0.1:$PORT/api/groups/$TMP_GROUP_ID" -H "Authorization: Bearer $AK" >/dev/null 2>&1
  fi

  # 2) 停掉进程
  [[ -n "$MOCK_PID" ]] && kill "$MOCK_PID" 2>/dev/null
  [[ -n "$SERVER_PID" ]] && { kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; }

  # 3) 硬删自己造的那几行 —— 软删会留一堆 __verify-* 垃圾, 跑几次就积一堆。
  #    只按记录下来的确切 ID 删, 且服务已停(无缓存陈旧问题)。
  if [[ -n "$TMP_GROUP_ID" ]] && command -v sqlite3 >/dev/null 2>&1 && [[ -f data/autogateway.db ]]; then
    sqlite3 data/autogateway.db \
      "DELETE FROM api_keys WHERE group_id=$TMP_GROUP_ID;
       DELETE FROM group_sub_groups WHERE sub_group_id=$TMP_GROUP_ID;
       DELETE FROM model_aliases WHERE group_id=$TMP_GROUP_ID;
       DELETE FROM groups WHERE id=$TMP_GROUP_ID;" >/dev/null 2>&1
    # 注意: 别名 id 与分组 id 是两套序列, 这里只按自己记录的别名 ID 删, 不能拿 group_id 去比
    [[ -n "$TMP_ALIAS_ID" ]] && sqlite3 data/autogateway.db \
      "DELETE FROM model_aliases WHERE id=$TMP_ALIAS_ID;" >/dev/null 2>&1
  fi

  rm -f "$MOCK_PY"
  [[ -n "$TMP_GROUP_ID" || -n "$MOCK_PID" || -n "$SERVER_PID" ]] && c_dim "  已清理临时 group/alias/挂载/密钥"
}
trap cleanup EXIT

# ── 工具 ────────────────────────────────────────────────────────────────
find_go() {
  if command -v go >/dev/null 2>&1; then command -v go; return; fi
  for p in /opt/homebrew/bin/go /usr/local/go/bin/go "$HOME/go/bin/go"; do
    [[ -x "$p" ]] && { echo "$p"; return; }
  done
  return 1
}

# jsonq <file> <python 表达式, d=解析后的 dict>
jsonq() {
  python3 - "$1" "$2" <<'PY'
import json,sys
try:
    d=json.load(open(sys.argv[1]))
except Exception:
    print("__PARSE_ERR__"); sys.exit(0)
try:
    print(eval(sys.argv[2], {"d": d, "json": json}))
except Exception as e:
    print("__EXPR_ERR__:%s" % e)
PY
}

wait_port() {
  local p="$1" i
  for i in $(seq 1 40); do
    curl -s "${NOPROXY[@]}" -o /dev/null "http://127.0.0.1:$p/" && return 0
    sleep 0.5
  done
  return 1
}

GO="$(find_go)" || { c_bad "找不到 go 工具链"; exit 1; }

head1 "L1 单元 / E2E 测试"
c_dim "  go test ./internal/..."
if "$GO" test ./internal/... >/tmp/verify-gotest.log 2>&1; then
  pass "go test ./internal/... 全绿"
else
  fail "go test 失败, 详见 /tmp/verify-gotest.log"; tail -20 /tmp/verify-gotest.log
fi

if [[ "$UNIT_ONLY" == "1" ]]; then
  head1 "汇总"; echo "  PASS=$PASS FAIL=$FAIL SKIP=$SKIP"; [[ "$FAIL" == "0" ]] && exit 0 || exit 1
fi

# ── 起服务 ──────────────────────────────────────────────────────────────
head1 "启动网关"
if [[ "$NO_BUILD" == "1" && -x "$BIN" ]]; then
  c_dim "  --no-build: 复用 $BIN"
else
  if "$GO" build -o "$BIN" . ; then pass "编译通过"; else fail "编译失败"; exit 1; fi
fi

if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  c_warn "  端口 $PORT 已被占用, 假定复用现有服务(不会由本脚本关闭)"
  EXTERNAL_SERVER=1
else
  EXTERNAL_SERVER=0
  "$BIN" >"$SERVER_LOG" 2>&1 &
  SERVER_PID=$!
  wait_port "$PORT" || { fail "服务未在 $PORT 就绪, 见 $SERVER_LOG"; tail -20 "$SERVER_LOG"; exit 1; }
  pass "服务已就绪 (pid $SERVER_PID)"
fi

AK="$(sqlite3 data/autogateway.db "SELECT setting_value FROM system_settings WHERE setting_key='auth_key' LIMIT 1;" 2>/dev/null)"
if [[ -z "$AK" ]]; then fail "读不到 DB 里的 auth_key (system_settings.auth_key)"; exit 1; fi
c_dim "  已从 DB 读取 auth_key (${#AK} 字符)"

# ── L2 正向: Anthropic 入站 → OpenAI 上游 ───────────────────────────────
head1 "L2 正向实机: Anthropic 入站 → OpenAI 上游"

FORWARD_ALIAS="${FORWARD_ALIAS_OPT:-}"
# 在 anthropic 聚合(id=3)的活跃子分组里挑候选 alias。
# 注意不能只取第一个 —— 聚合下往往挂着 embedding / reranker / tts 之类的非对话
# 模型, 打过去会拿到 400「暂不支持该接口」, 看起来像转译坏了, 其实只是选错了模型。
SUBS_JSON="$(mktemp)"
curl -s "${NOPROXY[@]}" "http://127.0.0.1:$PORT/api/groups/3/sub-groups" -H "Authorization: Bearer $AK" >"$SUBS_JSON"
SUB_IDS="$(python3 - "$SUBS_JSON" <<'PY'
import json,sys
try:
    d=json.load(open(sys.argv[1]))["data"]
    print(" ".join(str(x["group"]["id"]) for x in d if not x.get("deleted_at")))
except Exception:
    print("")
PY
)"
ALIASES_JSON="$(mktemp)"
curl -s "${NOPROXY[@]}" "http://127.0.0.1:$PORT/api/aliases" -H "Authorization: Bearer $AK" >"$ALIASES_JSON"

CANDIDATES=""
if [[ -n "$SUB_IDS" ]]; then
  CANDIDATES="$(python3 - "$ALIASES_JSON" $SUB_IDS <<'PY'
import json,sys
subs={int(x) for x in sys.argv[2:]}
# 明显的非对话模型, 直接排除(否则会误报成转译失败)
BAD=("embed","rerank","bge","mpnet","clip","tts","asr","whisper","ocr",
     "guard","nsfw","classif","prover","security","image","vidu","wan","tts")
def is_bad(n):
    l=n.lower()
    return any(b in l for b in BAD)
try:
    al=[a for a in json.load(open(sys.argv[1]))["data"]
        if a.get("enabled") and not a.get("deleted_at") and a.get("group_id") in subs]
except Exception:
    al=[]
# claude-* 优先(跨协议转译的典型用法), 其余按原序
al.sort(key=lambda a: 0 if a["alias"].lower().startswith("claude") else 1)
for a in al:
    if not is_bad(a["alias"]):
        print(a["alias"])
PY
)"
fi

if [[ -z "$FORWARD_ALIAS" && -z "$CANDIDATES" ]]; then
  skip "anthropic 聚合下找不到可用 alias, 跳过 L2 (先按 docs 配好 alias 再跑)"
else
  # 逐个候选试到 200 —— 有些 alias 指向的模型可能已停用/需付费, 换一个继续
  if [[ -z "$FORWARD_ALIAS" ]]; then
    TRIED=0
    for CAND in $CANDIDATES; do
      [[ "$TRIED" -ge 5 ]] && break
      TRIED=$((TRIED+1))
      curl -s "${NOPROXY[@]}" -o /tmp/fwd-ns.json -w '%{http_code}' -X POST \
        "http://127.0.0.1:$PORT/anthropic/v1/messages" \
        -H "Authorization: Bearer $AK" -H "anthropic-version: 2023-06-01" \
        -H "content-type: application/json" \
        -d "{\"model\":\"$CAND\",\"max_tokens\":24,\"messages\":[{\"role\":\"user\",\"content\":\"只回复:OK\"}]}" \
        >/tmp/fwd-ns.code 2>/dev/null
      if [[ "$(cat /tmp/fwd-ns.code)" == "200" \
            && "$(jsonq /tmp/fwd-ns.json 'd.get("type")')" == "message" ]]; then
        FORWARD_ALIAS="$CAND"; break
      fi
      c_dim "  候选 $CAND 不可用 (http=$(cat /tmp/fwd-ns.code)), 试下一个"
    done
  fi

  if [[ -z "$FORWARD_ALIAS" ]]; then
    fail "试遍候选 alias 都没有一个能返回 200; 最后一个响应: $(head -c 160 /tmp/fwd-ns.json)"
    c_dim "  提示: 用 --forward-alias <名称> 显式指定; 或确认 alias 指向的是可用的对话模型"
  else
    c_dim "  使用 alias: $FORWARD_ALIAS"
    # 非流式断言(上面循环里可能已经打过一次, 这里为统一口径重打一次)
    curl -s "${NOPROXY[@]}" -o /tmp/fwd-ns.json -w '%{http_code}' -X POST \
      "http://127.0.0.1:$PORT/anthropic/v1/messages" \
      -H "Authorization: Bearer $AK" -H "anthropic-version: 2023-06-01" \
      -H "content-type: application/json" \
      -d "{\"model\":\"$FORWARD_ALIAS\",\"max_tokens\":24,\"messages\":[{\"role\":\"user\",\"content\":\"只回复:OK\"}]}" \
      >/tmp/fwd-ns.code
    if [[ "$(cat /tmp/fwd-ns.code)" == "200" \
          && "$(jsonq /tmp/fwd-ns.json 'd.get("type")')" == "message" \
          && "$(jsonq /tmp/fwd-ns.json '"choices" in d')" == "False" \
          && "$(jsonq /tmp/fwd-ns.json '"usage" in d')" == "True" ]]; then
      pass "非流式: 200 + Anthropic 形状 (type=message / 无 choices / 有 usage)"
    else
      fail "非流式: http=$(cat /tmp/fwd-ns.code) body=$(head -c 200 /tmp/fwd-ns.json)"
    fi

    curl -s "${NOPROXY[@]}" -N -D /tmp/fwd-s.h -o /tmp/fwd-s.txt -w '%{http_code}' -X POST \
      "http://127.0.0.1:$PORT/anthropic/v1/messages" \
      -H "Authorization: Bearer $AK" -H "anthropic-version: 2023-06-01" \
      -H "content-type: application/json" \
      -d "{\"model\":\"$FORWARD_ALIAS\",\"max_tokens\":24,\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"只回复:OK\"}]}" \
      >/tmp/fwd-s.code
    if [[ "$(cat /tmp/fwd-s.code)" == "200" ]] \
       && grep -qa "^event: message_start" /tmp/fwd-s.txt \
       && grep -qa "^event: message_stop" /tmp/fwd-s.txt \
       && grep -qa "^event: message_delta" /tmp/fwd-s.txt; then
      pass "流式: 200 + 完整事件序列 (message_start … message_delta … message_stop)"
    else
      fail "流式: http=$(cat /tmp/fwd-s.code) 缺少 message_start/delta/stop"
    fi
    # usage 必须带真实 input_tokens(历史上这里恒为 0)
    IT="$(grep -a '"type":"message_delta"' /tmp/fwd-s.txt | tail -1 | python3 -c "
import sys,json,re
m=re.search(r'data: (\{.*\})', sys.stdin.read())
print(json.loads(m.group(1))['usage']['input_tokens'] if m else -1)" 2>/dev/null)"
    if [[ "${IT:-0}" -gt 0 ]]; then
      pass "流式 message_delta 带真实 input_tokens=$IT"
    else
      fail "流式 message_delta 的 input_tokens=${IT:-缺失} (应 > 0)"
    fi
    grep -qi "x-accel-buffering: no" /tmp/fwd-s.h \
      && pass "流式响应头含 X-Accel-Buffering: no" \
      || fail "流式响应头缺 X-Accel-Buffering: no (前置 nginx 会整条缓冲)"
  fi
fi

# ── L3 反向: OpenAI 入站 → Anthropic 上游(mock) ─────────────────────────
head1 "L3 反向实机: OpenAI 入站 → Anthropic 上游(mock)"

cat >"$MOCK_PY" <<'PY'
import json,sys
from http.server import BaseHTTPRequestHandler, HTTPServer
REPLY="MOCK_ANTHROPIC_OK"; IN=11; OUT=7
class H(BaseHTTPRequestHandler):
    protocol_version="HTTP/1.1"
    def log_message(self,*a): pass
    def do_POST(self):
        n=int(self.headers.get("Content-Length") or 0)
        raw=self.rfile.read(n) if n else b""
        try: body=json.loads(raw or b"{}")
        except Exception: body={}
        sys.stderr.write("MOCK_REQ "+json.dumps({"path":self.path,
            "x_api_key":bool(self.headers.get("x-api-key")),
            "anthropic_version":self.headers.get("anthropic-version"),
            "body":body},ensure_ascii=False)+"\n"); sys.stderr.flush()
        if body.get("stream"): self._stream()
        else: self._json()
    def _json(self):
        p={"id":"msg_mock_0001","type":"message","role":"assistant","model":"mock-anthropic-model",
           "content":[{"type":"text","text":REPLY}],"stop_reason":"end_turn","stop_sequence":None,
           "usage":{"input_tokens":IN,"output_tokens":OUT}}
        b=json.dumps(p).encode(); self.send_response(200)
        self.send_header("Content-Type","application/json")
        self.send_header("Content-Length",str(len(b))); self.end_headers(); self.wfile.write(b)
    def _stream(self):
        def ev(n,o): return ("event: %s\ndata: %s\n\n"%(n,json.dumps(o,ensure_ascii=False))).encode()
        fr=[ev("message_start",{"type":"message_start","message":{"id":"msg_mock_0002","type":"message",
              "role":"assistant","model":"mock-anthropic-model","content":[],"stop_reason":None,
              "stop_sequence":None,"usage":{"input_tokens":IN,"output_tokens":1}}}),
            ev("content_block_start",{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}),
            ev("content_block_delta",{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":REPLY}}),
            ev("content_block_stop",{"type":"content_block_stop","index":0}),
            ev("message_delta",{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":None},
               "usage":{"output_tokens":OUT}}),
            ev("message_stop",{"type":"message_stop"})]
        self.send_response(200); self.send_header("Content-Type","text/event-stream")
        self.send_header("Cache-Control","no-cache"); self.send_header("Transfer-Encoding","chunked")
        self.end_headers()
        for f in fr: self.wfile.write(b"%x\r\n"%len(f)+f+b"\r\n"); self.wfile.flush()
        self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
HTTPServer(("127.0.0.1",int(sys.argv[1])),H).serve_forever()
PY

python3 "$MOCK_PY" "$MOCK_PORT" >"$MOCK_LOG" 2>&1 &
MOCK_PID=$!
disown 2>/dev/null || true   # 免得 trap 杀掉时刷一行 "Terminated: 15"
if wait_port "$MOCK_PORT"; then pass "mock Anthropic 上游已就绪 (:$MOCK_PORT)"; else fail "mock 未就绪"; fi

GNAME="__verify-anthropic-upstream"
CREATE="$(curl -s "${NOPROXY[@]}" -X POST "http://127.0.0.1:$PORT/api/groups" \
  -H "Authorization: Bearer $AK" -H "Content-Type: application/json" -d "{
  \"name\":\"$GNAME\",\"display_name\":\"verify mock anthropic\",
  \"group_type\":\"standard\",\"channel_type\":\"anthropic\",
  \"upstreams\":[{\"url\":\"http://127.0.0.1:$MOCK_PORT/v1\",\"weight\":1}],
  \"test_model\":\"mock-anthropic-model\",
  \"available_models\":[\"mock-anthropic-model\"],
  \"validation_endpoint\":\"/v1/messages\"}")"
printf '%s' "$CREATE" >/tmp/verify-create.json
TMP_GROUP_ID="$(jsonq /tmp/verify-create.json 'd.get("data",{}).get("id","")')"
[[ -n "$TMP_GROUP_ID" ]] && pass "建临时 anthropic 分组 (id=$TMP_GROUP_ID)" || fail "建分组失败: $(head -c 200 /tmp/verify-create.json)"

if [[ -n "$TMP_GROUP_ID" ]]; then
  curl -s "${NOPROXY[@]}" -X POST "http://127.0.0.1:$PORT/api/keys/add-multiple" \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
    -d "{\"group_id\":$TMP_GROUP_ID,\"keys_text\":\"mock-anthropic-key-0001\"}" >/dev/null
  R="$(curl -s "${NOPROXY[@]}" -X POST "http://127.0.0.1:$PORT/api/groups/1/sub-groups" \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
    -d "{\"sub_groups\":[{\"group_id\":$TMP_GROUP_ID,\"weight\":1,\"priority\":100}]}")"
  [[ "$R" == *成功* ]] && { TMP_SUB_MOUNTED=1; pass "挂进 openai 聚合(id=1)"; } || fail "挂载失败: $(head -c 200 <<<"$R")"

  RA="$(curl -s "${NOPROXY[@]}" -X POST "http://127.0.0.1:$PORT/api/aliases" \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
    -d "{\"alias\":\"claude-reverse-verify\",\"group_id\":$TMP_GROUP_ID,\"real_model\":\"mock-anthropic-model\",\"weight\":1,\"priority\":1,\"enabled\":true}")"
  printf '%s' "$RA" >/tmp/verify-alias.json
  TMP_ALIAS_ID="$(jsonq /tmp/verify-alias.json 'd.get("data",{}).get("id","")')"
  [[ -n "$TMP_ALIAS_ID" ]] && pass "建 alias claude-reverse-verify (id=$TMP_ALIAS_ID)" || fail "建 alias 失败"
fi

if [[ -n "$TMP_ALIAS_ID" ]]; then
  RV='{"model":"claude-reverse-verify","max_tokens":24,"messages":[{"role":"user","content":"hi"}]}'
  curl -s "${NOPROXY[@]}" -o /tmp/rev-ns.json -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/openai/v1/chat/completions" \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" -d "$RV" >/tmp/rev-ns.code
  if [[ "$(cat /tmp/rev-ns.code)" == "200" \
        && "$(jsonq /tmp/rev-ns.json 'd.get("object")')" == "chat.completion" \
        && "$(jsonq /tmp/rev-ns.json 'd["choices"][0]["message"]["content"]')" == "MOCK_ANTHROPIC_OK" ]]; then
    pass "非流式: 200 + OpenAI 形状 + 内容正确"
  else
    fail "非流式: http=$(cat /tmp/rev-ns.code) body=$(head -c 200 /tmp/rev-ns.json)"
  fi

  # 断言上游真的收到了 Anthropic 形状的请求(这才是"转译"的证据)
  LASTREQ="$(grep -a '^MOCK_REQ ' "$MOCK_LOG" | tail -1)"
  if [[ "$LASTREQ" == *'"path": "/v1/messages"'* \
        && "$LASTREQ" == *'"x_api_key": true'* \
        && "$LASTREQ" == *'"type": "text"'* ]]; then
    pass "上游收到 Anthropic 形状请求 (/v1/messages + x-api-key + content 转 block)"
  else
    fail "上游请求形状不符: $(head -c 220 <<<"$LASTREQ")"
  fi

  curl -s "${NOPROXY[@]}" -N -D /tmp/rev-s.h -o /tmp/rev-s.txt -w '%{http_code}' -X POST \
    "http://127.0.0.1:$PORT/openai/v1/chat/completions" \
    -H "Authorization: Bearer $AK" -H "Content-Type: application/json" \
    -d '{"model":"claude-reverse-verify","max_tokens":24,"stream":true,"messages":[{"role":"user","content":"hi"}]}' >/tmp/rev-s.code
  if [[ "$(cat /tmp/rev-s.code)" == "200" ]] \
     && grep -qa '"object":"chat.completion.chunk"' /tmp/rev-s.txt \
     && grep -qa '^data: \[DONE\]' /tmp/rev-s.txt \
     && grep -qa 'MOCK_ANTHROPIC_OK' /tmp/rev-s.txt; then
    pass "流式: 200 + chat.completion.chunk 序列 + [DONE] + 内容正确"
  else
    fail "流式: http=$(cat /tmp/rev-s.code)"
  fi
  grep -qa '"stream": true' <<<"$(grep -a '^MOCK_REQ ' "$MOCK_LOG" | tail -1)" \
    && pass "stream:true 已透传给上游" \
    || fail "stream:true 未透传到上游"
fi

# ── 汇总 ────────────────────────────────────────────────────────────────
head1 "汇总"
echo "  PASS=$PASS  FAIL=$FAIL  SKIP=$SKIP"
[[ "$FAIL" == "0" ]] && { c_ok "全部通过"; exit 0; } || { c_bad "存在失败项"; exit 1; }
