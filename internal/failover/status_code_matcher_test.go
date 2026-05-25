package failover

import "testing"

func TestStatusCodeMatcher_ZeroValue(t *testing.T) {
	var m StatusCodeMatcher
	if m.Match(500) {
		t.Errorf("zero-value matcher must not match anything, but matched 500")
	}
	if !m.IsEmpty() {
		t.Errorf("zero-value matcher must report IsEmpty=true")
	}
}

func TestParseStatusCodeMatcher_EmptyAndWhitespace(t *testing.T) {
	for _, spec := range []string{"", "   ", "  ,  , \n,"} {
		m, err := ParseStatusCodeMatcher(spec)
		if err != nil {
			t.Errorf("spec %q must parse without error, got %v", spec, err)
		}
		if !m.IsEmpty() {
			t.Errorf("spec %q must produce empty matcher", spec)
		}
	}
}

func TestParseStatusCodeMatcher_SingleAndRange(t *testing.T) {
	m, err := ParseStatusCodeMatcher("404, 500-599, 429")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	cases := map[int]bool{
		403: false, 404: true, 405: false, // 单值边界
		428: false, 429: true, 430: false, // 单值
		499: false, 500: true, 550: true, 599: true, 600: false, // 区间
		100: false, 999: false,
	}
	for code, want := range cases {
		if got := m.Match(code); got != want {
			t.Errorf("Match(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestParseStatusCodeMatcher_Merging(t *testing.T) {
	// 重叠 + 相邻区间必须合并 (400-405 + 405-410 → 400-410)
	m, err := ParseStatusCodeMatcher("400-405, 405-410, 411-415")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	for code := 400; code <= 415; code++ {
		if !m.Match(code) {
			t.Errorf("after merging, Match(%d) should be true", code)
		}
	}
	if m.Match(399) || m.Match(416) {
		t.Errorf("merging must not extend beyond original bounds")
	}
}

func TestParseStatusCodeMatcher_NewlineAsComma(t *testing.T) {
	// 用户粘贴多行配置, 换行视作逗号
	m, err := ParseStatusCodeMatcher("404\n500-599\n429")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !m.Match(404) || !m.Match(429) || !m.Match(550) {
		t.Errorf("newline spec must parse like comma-separated")
	}
}

func TestParseStatusCodeMatcher_DefaultSpec(t *testing.T) {
	// 默认 spec "400-403,405-999" — 关键: 不命中 404, 命中 400/429/500/999.
	m, err := ParseStatusCodeMatcher("400-403,405-999")
	if err != nil {
		t.Fatalf("default spec must parse, got %v", err)
	}
	cases := map[int]bool{
		200: false, 301: false, 399: false,
		400: true, 401: true, 402: true, 403: true,
		404: false, // 关键: alias-routed 404 在 proxy 层另行处理
		405: true, 429: true, 500: true, 502: true, 999: true,
	}
	for code, want := range cases {
		if got := m.Match(code); got != want {
			t.Errorf("default spec Match(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestParseStatusCodeMatcher_Errors(t *testing.T) {
	bad := []string{
		"abc",          // 非数字
		"99",           // 低于 100
		"1000",         // 高于 999
		"500-abc",      // 区间含非数字
		"500-400",      // 起始 > 结束
		"500-",         // 缺右值
		"-500",         // 缺左值
		"500-501-502",  // 多于一个 -
	}
	for _, spec := range bad {
		if _, err := ParseStatusCodeMatcher(spec); err == nil {
			t.Errorf("spec %q must return error, got nil", spec)
		}
	}
}
