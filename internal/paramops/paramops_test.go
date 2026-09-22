package paramops

import (
	"encoding/json"
	"reflect"
	"testing"
)

func applyOps(t *testing.T, body string, specJSON string) string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(specJSON), &raw); err != nil {
		t.Fatalf("spec JSON: %v", err)
	}
	spec, err := ParseSpec(raw)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	out, err := Apply([]byte(body), spec, Vars{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return string(out)
}

// sameJSON 按语义比较两个 JSON, 不受 key 顺序影响。
// json.Marshal 会把 map 的 key 按字母序排, 所以不能拿字符串直接比。
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got is not valid JSON (%s): %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not valid JSON (%s): %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	if !sameJSON(t, got, want) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// ---------- 路径 ----------

func TestPathGetSetDelete(t *testing.T) {
	root := map[string]any{
		"temperature": 0.7,
		"messages": []any{
			map[string]any{"role": "user", "content": "a"},
			map[string]any{"role": "user", "content": "b"},
		},
		"metadata": map[string]any{"user": map[string]any{"name": "zac"}},
	}

	if v, ok := GetPath(root, "metadata.user.name"); !ok || v != "zac" {
		t.Errorf("nested get = %v %v", v, ok)
	}
	if v, ok := GetPath(root, "messages.0.content"); !ok || v != "a" {
		t.Errorf("index 0 = %v %v", v, ok)
	}
	if v, ok := GetPath(root, "messages.-1.content"); !ok || v != "b" {
		t.Errorf("index -1 = %v %v", v, ok)
	}
	if _, ok := GetPath(root, "messages.9.content"); ok {
		t.Error("out of range should not be found")
	}
	if _, ok := GetPath(root, "nope"); ok {
		t.Error("missing key should not be found")
	}

	if err := SetPath(root, "metadata.user.name", "kai"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, _ := GetPath(root, "metadata.user.name"); v != "kai" {
		t.Errorf("after set = %v", v)
	}
	if err := SetPath(root, "nope.deep", 1); err == nil {
		t.Error("setting under a missing parent should error")
	}
	if !DeletePath(root, "temperature") {
		t.Error("delete existing key should report true")
	}
	if DeletePath(root, "temperature") {
		t.Error("deleting twice should report false")
	}
}

// ---------- spec 解析 / 向后兼容 ----------

func TestParseSpecNotAdvanced(t *testing.T) {
	spec, err := ParseSpec(map[string]any{"temperature": 0.3})
	if err != nil || spec != nil {
		t.Errorf("flat override must not be parsed as advanced: %v %v", spec, err)
	}
	spec, err = ParseSpec(map[string]any{"*": map[string]any{"a": 1}})
	if err != nil || spec != nil {
		t.Errorf("nested override must not be parsed as advanced: %v %v", spec, err)
	}
}

func TestParseSpecRejectsBadSpec(t *testing.T) {
	cases := map[string]string{
		`{"operations":[]}`:                "empty operations",
		`{"operations":[{"path":"a"}]}`:    "missing mode",
		`{"operations":[{"mode":"set"}]}`:  "set without path",
		`{"operations":[{"mode":"nope"}]}`: "unknown mode",
		`{"operations":[{"mode":"move"}]}`: "move without from/to",
		`{"operations":[{"mode":"copy","path":"a","conditions":[{"mode":"bogus"}]}]}`: "bad condition mode",
	}
	for raw, why := range cases {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("%s: bad test JSON: %v", why, err)
		}
		if _, err := ParseSpec(m); err == nil {
			t.Errorf("%s: expected error, got nil", why)
		}
	}
}

// ---------- 操作模式 ----------

func TestOpsSet(t *testing.T) {
	body := `{"model":"m","temperature":0.7}`
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"temperature","value":0.1}]}`),
		`{"model":"m","temperature":0.1}`)
	// keep_origin: 已有值则跳过
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"temperature","value":0.1,"keep_origin":true}]}`), body)
	// keep_origin 但字段不存在 → 仍然写入
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"top_p","value":0.9,"keep_origin":true}]}`),
		`{"model":"m","temperature":0.7,"top_p":0.9}`)
}

func TestOpsDelete(t *testing.T) {
	got := applyOps(t, `{"a":1,"b":2}`, `{"operations":[{"mode":"delete","path":"a"}]}`)
	if got != `{"b":2}` {
		t.Errorf("delete = %s", got)
	}
}

func TestOpsMoveAndCopy(t *testing.T) {
	body := `{"system":"be nice","messages":[{"role":"user","content":"hi"}]}`
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"move","from":"system","to":"instructions"}]}`),
		`{"instructions":"be nice","messages":[{"role":"user","content":"hi"}]}`)
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"copy","from":"system","to":"instructions"}]}`),
		`{"instructions":"be nice","messages":[{"role":"user","content":"hi"}],"system":"be nice"}`)
}

func TestOpsAppendPrepend(t *testing.T) {
	// 字符串
	assertJSON(t, applyOps(t, `{"q":"hi"}`, `{"operations":[{"mode":"append","path":"q","value":"!"}]}`), `{"q":"hi!"}`)
	assertJSON(t, applyOps(t, `{"q":"hi"}`, `{"operations":[{"mode":"prepend","path":"q","value":"> "}]}`), `{"q":"> hi"}`)
	// 数组: 单个元素
	assertJSON(t, applyOps(t, `{"messages":[{"role":"user","content":"hi"}]}`,
		`{"operations":[{"mode":"prepend","path":"messages","value":{"role":"system","content":"be nice"}}]}`),
		`{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"hi"}]}`)
	// 对象: 合并
	assertJSON(t, applyOps(t, `{"extra":{"a":1}}`, `{"operations":[{"mode":"append","path":"extra","value":{"b":2}}]}`),
		`{"extra":{"a":1,"b":2}}`)
	// 数组: 一次追加多个元素
	assertJSON(t, applyOps(t, `{"a":[1]}`, `{"operations":[{"mode":"append","path":"a","value":[2,3]}]}`),
		`{"a":[1,2,3]}`)
}

func TestOpsStringTransforms(t *testing.T) {
	cases := []struct {
		name string
		body string
		op   string
		want string
	}{
		{"trim_prefix", `{"m":"openai/gpt-4"}`, `{"mode":"trim_prefix","path":"m","value":"openai/"}`, `{"m":"gpt-4"}`},
		{"trim_suffix", `{"m":"gpt-4-latest"}`, `{"mode":"trim_suffix","path":"m","value":"-latest"}`, `{"m":"gpt-4"}`},
		{"ensure_prefix", `{"m":"gpt-4"}`, `{"mode":"ensure_prefix","path":"m","value":"openai/"}`, `{"m":"openai/gpt-4"}`},
		{"ensure_prefix_idempotent", `{"m":"openai/gpt-4"}`, `{"mode":"ensure_prefix","path":"m","value":"openai/"}`, `{"m":"openai/gpt-4"}`},
		{"ensure_suffix", `{"m":"gpt-4"}`, `{"mode":"ensure_suffix","path":"m","value":"-latest"}`, `{"m":"gpt-4-latest"}`},
		{"trim_space", `{"m":"  gpt-4  "}`, `{"mode":"trim_space","path":"m"}`, `{"m":"gpt-4"}`},
		{"to_lower", `{"m":"GPT-4"}`, `{"mode":"to_lower","path":"m"}`, `{"m":"gpt-4"}`},
		{"to_upper", `{"m":"gpt-4"}`, `{"mode":"to_upper","path":"m"}`, `{"m":"GPT-4"}`},
		{"replace", `{"m":"gpt-4-turbo"}`, `{"mode":"replace","path":"m","from":"-turbo","to":""}`, `{"m":"gpt-4"}`},
		{"regex_replace", `{"m":"gpt-4"}`, `{"mode":"regex_replace","path":"m","from":"^gpt-","to":"openai/gpt-"}`, `{"m":"openai/gpt-4"}`},
	}
	for _, c := range cases {
		assertJSON(t, applyOps(t, c.body, `{"operations":[`+c.op+`]}`), c.want)
	}
}

// ---------- 条件 ----------

func TestConditions(t *testing.T) {
	body := `{"model":"gpt-4","max_tokens":100,"stream":true}`

	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"temperature","value":0.1,"conditions":[{"path":"model","mode":"contains","value":"gpt-4"}]}]}`),
		`{"max_tokens":100,"model":"gpt-4","stream":true,"temperature":0.1}`)

	// 条件不满足 → 跳过, body 不变
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"temperature","value":0.1,"conditions":[{"path":"model","mode":"contains","value":"claude"}]}]}`), body)

	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"top_p","value":0.5,"conditions":[{"path":"max_tokens","mode":"gt","value":50}]}]}`),
		`{"max_tokens":100,"model":"gpt-4","stream":true,"top_p":0.5}`)

	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"top_p","value":0.5,"conditions":[{"path":"max_tokens","mode":"lt","value":50}]}]}`), body)

	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"top_p","value":0.5,"conditions":[{"path":"model","mode":"contains","value":"claude","invert":true}]}]}`),
		`{"max_tokens":100,"model":"gpt-4","stream":true,"top_p":0.5}`)
}

func TestConditionLogic(t *testing.T) {
	body := `{"model":"gpt-4","max_tokens":100}`
	// AND(默认): 两个条件都要满足 → 第二个不满足, 不生效
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"t","value":1,"conditions":[{"path":"model","mode":"prefix","value":"gpt"},{"path":"model","mode":"prefix","value":"claude"}]}]}`), body)
	// OR: 任一满足即生效
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"t","value":1,"logic":"OR","conditions":[{"path":"model","mode":"prefix","value":"gpt"},{"path":"model","mode":"prefix","value":"claude"}]}]}`),
		`{"max_tokens":100,"model":"gpt-4","t":1}`)
}

func TestConditionMissingKey(t *testing.T) {
	body := `{"model":"gpt-4"}`
	// 默认: 路径不存在 → 条件不通过
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"t","value":1,"conditions":[{"path":"nope","mode":"full","value":"x"}]}]}`), body)
	// pass_missing_key: 路径不存在 → 条件通过
	assertJSON(t, applyOps(t, body, `{"operations":[{"mode":"set","path":"t","value":1,"conditions":[{"path":"nope","mode":"full","value":"x","pass_missing_key":true}]}]}`),
		`{"model":"gpt-4","t":1}`)
}

func TestBuiltinVars(t *testing.T) {
	// model 是内置变量, 但请求体里没有它时也能用
	var raw map[string]any
	specJSON := `{"operations":[{"mode":"set","path":"temperature","value":0.2,"conditions":[{"path":"upstream_model","mode":"suffix","value":"Qwen3-8B"}]}]}`
	if err := json.Unmarshal([]byte(specJSON), &raw); err != nil {
		t.Fatal(err)
	}
	spec, err := ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"messages":[{"role":"user","content":"hi"}]}`
	// 命中: upstream_model 以 Qwen3-8B 结尾
	out, err := Apply([]byte(body), spec, Vars{UpstreamModel: "Qwen/Qwen3-8B"})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, string(out), `{"messages":[{"role":"user","content":"hi"}],"temperature":0.2}`)

	// 不命中: 换一个上游模型名
	out, err = Apply([]byte(body), spec, Vars{UpstreamModel: "gpt-4o"})
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, string(out), body)
}

// ---------- 出错即整体放弃 ----------

func TestApplyFailsAtomically(t *testing.T) {
	body := `{"model":"m","temperature":0.7}`
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"operations":[{"mode":"set","path":"temperature","value":0.1},{"mode":"set","path":"nope.deep","value":1}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	spec, err := ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Apply([]byte(body), spec, Vars{})
	if err == nil {
		t.Fatal("expected error from the second operation")
	}
	if string(out) != body {
		t.Errorf("on error the original body must be returned unchanged, got %s", out)
	}
}

func TestApplyPassesThroughNonObject(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"operations":[{"mode":"set","path":"a","value":1}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	spec, _ := ParseSpec(raw)
	out, err := Apply([]byte(`[1,2,3]`), spec, Vars{})
	if err == nil {
		t.Error("non-object body should error, not be silently rewritten")
	}
	if string(out) != `[1,2,3]` {
		t.Errorf("non-object body must pass through unchanged, got %s", out)
	}
}
