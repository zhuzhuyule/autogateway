package channel

import (
	"net/url"
	"testing"
)

func TestBuildUpstreamURLStripsProxyPrefix(t *testing.T) {
	base, _ := url.Parse("https://api.groq.com/openai")
	original, _ := url.Parse("http://localhost:3001/proxy/groq/v1/chat/completions")
	ch := &BaseChannel{
		Name:      "test",
		Upstreams: []UpstreamInfo{{URL: base, Weight: 1}},
	}

	got, err := ch.BuildUpstreamURL(original, "groq")
	if err != nil {
		t.Fatalf("BuildUpstreamURL returned error: %v", err)
	}

	want := "https://api.groq.com/openai/v1/chat/completions"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestBuildUpstreamURLStripsSystemShortcutPrefix(t *testing.T) {
	base, _ := url.Parse("https://api.groq.com/openai")
	original, _ := url.Parse("http://localhost:3001/openai/v1/models")
	ch := &BaseChannel{
		Name:      "test",
		Upstreams: []UpstreamInfo{{URL: base, Weight: 1}},
	}

	got, err := ch.BuildUpstreamURL(original, "openai")
	if err != nil {
		t.Fatalf("BuildUpstreamURL returned error: %v", err)
	}

	want := "https://api.groq.com/openai/v1/models"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestBuildUpstreamURLDedupesVersionAfterShortcutStrip(t *testing.T) {
	base, _ := url.Parse("https://openrouter.ai/api/v1")
	original, _ := url.Parse("http://localhost:3001/openai/v1/chat/completions")
	ch := &BaseChannel{
		Name:      "test",
		Upstreams: []UpstreamInfo{{URL: base, Weight: 1}},
	}

	got, err := ch.BuildUpstreamURL(original, "openai")
	if err != nil {
		t.Fatalf("BuildUpstreamURL returned error: %v", err)
	}

	want := "https://openrouter.ai/api/v1/chat/completions"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

// JoinUpstreamURL 是所有上游入口共享的拼接方法 — 必须自动去重 /v1 / /v1beta,
// 避免 ValidateKey 拼出 /v1/v1/chat/completions 全协议 404.
func TestJoinUpstreamURLDedupesV1(t *testing.T) {
	base, _ := url.Parse("https://api.openai.com/v1")
	ch := &BaseChannel{Name: "test"}

	got := ch.JoinUpstreamURL(base, "/v1/chat/completions").String()
	want := "https://api.openai.com/v1/chat/completions"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

func TestJoinUpstreamURLDedupesV1beta(t *testing.T) {
	base, _ := url.Parse("https://generativelanguage.googleapis.com/v1beta")
	ch := &BaseChannel{Name: "test"}

	got := ch.JoinUpstreamURL(base, "/v1beta/models/gemini-2.5-flash:generateContent").String()
	want := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

// xfyun MaaS API 的 baseUrl 末段是 /v2, 而 OpenAI-compat 验证端点模板是
// /v1/chat/completions; 必须把 request 的 /v1 段砍掉, 否则就拼成
// /v2/v1/chat/completions 全协议 404.
func TestJoinUpstreamURLBaseVersionWinsOverRequest(t *testing.T) {
	base, _ := url.Parse("https://maas-api.cn-huabei-1.xf-yun.com/v2")
	ch := &BaseChannel{Name: "test"}

	got := ch.JoinUpstreamURL(base, "/v1/chat/completions").String()
	want := "https://maas-api.cn-huabei-1.xf-yun.com/v2/chat/completions"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

// base=/v1beta (Gemini openai-compat 子路径) + endpoint=/v1/chat/completions
// 应该砍掉 request 的 /v1, 拼成 /v1beta/chat/completions.
func TestJoinUpstreamURLV1betaBaseStripsV1Request(t *testing.T) {
	base, _ := url.Parse("https://generativelanguage.googleapis.com/v1beta")
	ch := &BaseChannel{Name: "test"}

	got := ch.JoinUpstreamURL(base, "/v1/chat/completions").String()
	want := "https://generativelanguage.googleapis.com/v1beta/chat/completions"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}

// "#" 是用户级 UI escape, 持久化进库, 拼上游 URL 时必须丢弃 — String() 不能
// 出现 "#" 否则 HTTP server 会看到莫名 fragment.
func TestJoinUpstreamURLDropsFragment(t *testing.T) {
	base, _ := url.Parse("https://api.openai.com/v1#")
	ch := &BaseChannel{Name: "test"}

	got := ch.JoinUpstreamURL(base, "/v1/chat/completions").String()
	want := "https://api.openai.com/v1/chat/completions"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}
