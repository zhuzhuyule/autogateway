package errors

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		msg    string
		want   Category
	}{
		// —— 用户现场:400 音频格式错,过去误拉黑好 key ——
		{"audio format 400", 400, "Invalid audio file. Format not recognised.", CategoryRequestError},
		{"audio format no status", 0, "audio file is not valid, Format not recognised", CategoryRequestError},

		// —— key/账号问题:该计入 ——
		{"401 auth", 401, "Unauthorized", CategoryKeyError},
		{"403 forbidden", 403, "forbidden", CategoryKeyError},
		{"402 payment", 402, "payment required", CategoryKeyError},
		{"invalid api key text overrides 400", 400, "Incorrect API key provided", CategoryKeyError},
		{"insufficient quota (billing, not rate)", 429, "You exceeded your current quota, please check your plan and billing details.", CategoryKeyError},
		{"insufficient_quota type in body", 429, "insufficient_quota", CategoryKeyError},

		// —— 限流:不计入 ——
		{"429 rate limit", 429, "Rate limit reached for gpt-4", CategoryRateLimited},
		{"gemini resource exhausted", 429, "Resource has been exhausted (e.g. check quota).", CategoryRateLimited},
		{"anthropic overloaded", 529, "Overloaded", CategoryRateLimited},
		{"too many requests text", 400, "too many requests, slow down", CategoryRateLimited},

		// —— 请求错:不计入 + 快速失败 ——
		{"400 bad request generic", 400, "Bad Request", CategoryRequestError},
		{"context length", 400, "This model's maximum context length is 8192 tokens.", CategoryRequestError},
		{"reduce length (old uncounted #2)", 200, "please reduce the length of the messages", CategoryRequestError},
		{"404 model not found", 404, "The model `gpt-5` does not exist", CategoryRequestError},
		{"422 unprocessable", 422, "unprocessable entity", CategoryRequestError},

		// —— 上游故障:不计入,但不快速失败(仍 failover) ——
		{"500 provider", 500, "Internal Server Error", CategoryProviderError},
		{"502 bad gateway", 502, "bad gateway", CategoryProviderError},

		// —— Unknown:保守计入 ——
		{"transport error no status unknown text", 0, "dial tcp: connection refused", CategoryUnknown},
		{"408 timeout", 408, "request timeout", CategoryUnknown},
		{"empty msg no status", 0, "", CategoryUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.status, tc.msg); got != tc.want {
				t.Errorf("Classify(%d, %q) = %v, want %v", tc.status, tc.msg, got, tc.want)
			}
		})
	}
}

func TestCategoryCountsAgainstKey(t *testing.T) {
	counts := map[Category]bool{
		CategoryKeyError:      true,
		CategoryUnknown:       true,
		CategoryRequestError:  false,
		CategoryRateLimited:   false,
		CategoryProviderError: false,
	}
	for cat, want := range counts {
		if got := cat.CountsAgainstKey(); got != want {
			t.Errorf("%v.CountsAgainstKey() = %v, want %v", cat, got, want)
		}
	}
}

func TestCategoryShouldFailFast(t *testing.T) {
	for _, cat := range []Category{CategoryUnknown, CategoryKeyError, CategoryRateLimited, CategoryProviderError} {
		if cat.ShouldFailFast() {
			t.Errorf("%v.ShouldFailFast() = true, want false", cat)
		}
	}
	if !CategoryRequestError.ShouldFailFast() {
		t.Error("CategoryRequestError.ShouldFailFast() = false, want true")
	}
}

// IsUnCounted 是 Classify 的薄封装,这里锁死向后兼容:两条历史 uncounted
// 子串仍判 uncounted,普通 key 错仍计入。
func TestIsUnCountedBackCompat(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"resource has been exhausted", true},                  // 历史 uncounted #1
		{"please reduce the length of the messages", true},     // 历史 uncounted #2
		{"Rate limit reached", true},                           // 新增:限流也不计
		{"Invalid audio file. Format not recognised.", true},   // 新增:请求错也不计
		{"Incorrect API key provided", false},                  // key 错仍计入
		{"some unclassifiable error", false},                   // Unknown 计入
		{"", false},                                            // 空串
	}
	for _, tc := range cases {
		if got := IsUnCounted(tc.msg); got != tc.want {
			t.Errorf("IsUnCounted(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
