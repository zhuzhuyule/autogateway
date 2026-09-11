package apicompat

import "testing"

// Anthropic 对历史消息有三条不变式,下面的用例逐一验证修复逻辑:
//  1. 每个 tool_result 必须紧跟含对应 tool_use 的 assistant 消息
//  2. 每个 tool_use 必须被应答(未应答 → 400)
//  3. user / assistant 必须交替

func TestPairingRepair_OrphanToolUseDropped(t *testing.T) {
	// 只有调用没有结果 → 上游会 400,必须整体删除这条 assistant 消息。
	in := []AnthropicMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: []AnthropicContentBlock{
			{Type: "tool_use", ID: "t1", Name: "f", Input: map[string]any{}},
		}},
	}
	got := normalizeAnthropicMessages(in)
	if len(got) != 1 {
		t.Fatalf("messages = %d, want 1 (%+v)", len(got), got)
	}
	if got[0].Role != "user" {
		t.Errorf("role = %q, want user", got[0].Role)
	}
}

func TestPairingRepair_OrphanToolResultDropped(t *testing.T) {
	// 只有结果没有对应调用 → 孤儿 result 必须丢弃。
	in := []AnthropicMessage{
		{Role: "user", Content: "q"},
		{Role: "user", Content: []AnthropicContentBlock{
			{Type: "tool_result", ToolUseID: "ghost", Content: "x"},
		}},
	}
	got := normalizeAnthropicMessages(in)
	if len(got) != 1 {
		t.Fatalf("messages = %d, want 1 (%+v)", len(got), got)
	}
	for _, b := range anthropicBlocks(got[0].Content) {
		if b.Type == "tool_result" {
			t.Errorf("orphan tool_result survived: %+v", b)
		}
	}
}

func TestPairingRepair_ResultFollowsItsCall(t *testing.T) {
	in := []AnthropicMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: []AnthropicContentBlock{
			{Type: "tool_use", ID: "t1", Name: "f"},
		}},
		{Role: "user", Content: []AnthropicContentBlock{
			{Type: "tool_result", ToolUseID: "t1", Content: "r1"},
		}},
		{Role: "user", Content: "after"},
	}
	got := normalizeAnthropicMessages(in)

	// 期望: user(q) / assistant(tool_use t1) / user(tool_result + after 合并)
	if len(got) != 3 {
		t.Fatalf("messages = %d, want 3 (%+v)", len(got), got)
	}
	if got[1].Role != "assistant" {
		t.Fatalf("messages[1].role = %q", got[1].Role)
	}
	blocks := anthropicBlocks(got[2].Content)
	if len(blocks) == 0 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "t1" {
		t.Fatalf("messages[2] blocks = %+v, want tool_result t1 first", blocks)
	}
}

func TestPairingRepair_AlternationEnforced(t *testing.T) {
	in := []AnthropicMessage{
		{Role: "user", Content: "a"},
		{Role: "user", Content: "b"},
		{Role: "assistant", Content: "c"},
		{Role: "assistant", Content: "d"},
	}
	got := normalizeAnthropicMessages(in)
	for i := 1; i < len(got); i++ {
		if got[i].Role == got[i-1].Role {
			t.Errorf("consecutive same role at %d: %q %q", i, got[i-1].Role, got[i].Role)
		}
	}
	if len(got) != 2 {
		t.Errorf("messages = %d, want 2 after merge", len(got))
	}
}

func TestPairingRepair_InterleavedHistory(t *testing.T) {
	// 真实场景:tool_use 与它的 result 之间插了别的内容(审批通知等)。
	// 朴素逐条转换会破坏配对,修复后必须仍然成对。
	in := []AnthropicMessage{
		{Role: "assistant", Content: []AnthropicContentBlock{
			{Type: "tool_use", ID: "t1", Name: "f"},
		}},
		{Role: "user", Content: "approve"},
		{Role: "user", Content: []AnthropicContentBlock{
			{Type: "tool_result", ToolUseID: "t1", Content: "done"},
		}},
	}
	got := normalizeAnthropicMessages(in)

	var sawUse, sawResult bool
	for _, m := range got {
		for _, b := range anthropicBlocks(m.Content) {
			switch b.Type {
			case "tool_use":
				sawUse = true
			case "tool_result":
				sawResult = true
				if b.ToolUseID != "t1" {
					t.Errorf("tool_result id = %q", b.ToolUseID)
				}
			}
		}
	}
	if !sawUse || !sawResult {
		t.Fatalf("pair lost: use=%v result=%v (%+v)", sawUse, sawResult, got)
	}
}

func TestClampMaxTokens(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultMaxTokens},
		{-5, defaultMaxTokens},
		{1, 1},
		{4096, 4096},
		{100000, maxTokensCap},
	}
	for _, c := range cases {
		if got := clampMaxTokens(c.in); got != c.want {
			t.Errorf("clampMaxTokens(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNormalizeToolArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", emptyToolArgs},
		{"{broken", emptyToolArgs}, // 非法 JSON 整体丢弃
		{`{"a":1}`, `{"a":1}`},
	}
	for _, c := range cases {
		if got := normalizeToolArgs(c.in); got != c.want {
			t.Errorf("normalizeToolArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeToolSchema(t *testing.T) {
	if got := normalizeToolSchema(nil); got["type"] != "object" || got["properties"] == nil {
		t.Errorf("nil schema = %+v", got)
	}
	// 有 required 但缺 properties:保留 required,只补 properties
	got := normalizeToolSchema(map[string]any{"type": "object", "required": []any{"a"}})
	if got["properties"] == nil {
		t.Errorf("missing properties not filled: %+v", got)
	}
	if _, ok := got["required"]; !ok {
		t.Errorf("existing field dropped: %+v", got)
	}
}

func TestDecodeToolArgs(t *testing.T) {
	if got := decodeToolArgs(""); len(got) != 0 {
		t.Errorf("empty args = %+v, want empty map", got)
	}
	if got := decodeToolArgs("not json"); len(got) != 0 {
		t.Errorf("invalid args = %+v, want empty map", got)
	}
	if got := decodeToolArgs(`{"a":1}`); got["a"] != float64(1) {
		t.Errorf("args = %+v", got)
	}
}

func TestAnthropicSystemText(t *testing.T) {
	if got := anthropicSystemText(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	if got := anthropicSystemText("plain"); got != "plain" {
		t.Errorf("string = %q", got)
	}
	got := anthropicSystemText([]AnthropicContentBlock{{Type: "text", Text: "a"}, {Type: "text", Text: "b"}})
	if got != "a\n\nb" {
		t.Errorf("blocks = %q, want %q", got, "a\n\nb")
	}
}
