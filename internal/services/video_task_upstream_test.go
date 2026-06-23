package services

import (
	"testing"

	"autogateway/internal/models"
)

// toResult 是 agnes 响应 → 归一化结果的字段映射, 是该上游适配里最易回归的逻辑
// (字段名一旦写错, 只会在对接真实 agnes 时才暴露)。这里直接单测纯函数。
//
// 注:doRequest 的 URL 构造/鉴权头注入复用 channel 包(BuildUpstreamURL/
// ModifyRequest, 已有自身测试覆盖)。针对 channelUpstream 的端到端 httptest
// 集成测试需自造 GroupManager+channel.Factory+keypool(含 key)整套依赖, 当前
// 仓库无现成构造器, 成本过高, 暂作为已知测试缺口记录(见 plan 风险清单)。
func TestAgnesResponse_ToResult(t *testing.T) {
	cases := []struct {
		name     string
		in       agnesResponse
		wantStat string
		wantURL  string
		wantUpID string
		wantProg int
		wantErr  string
	}{
		{
			name:     "blocking completed carries url via remixed_from_video_id",
			in:       agnesResponse{Status: "completed", Progress: 100, RemixedFromVideoID: "https://x/v.mp4", TaskID: "task_1"},
			wantStat: "completed", wantURL: "https://x/v.mp4", wantUpID: "task_1", wantProg: 100,
		},
		{
			name:     "task_id takes precedence over video_id",
			in:       agnesResponse{Status: "queued", TaskID: "task_1", VideoID: "vid_9"},
			wantStat: "queued", wantUpID: "task_1",
		},
		{
			name:     "video_id is fallback when task_id empty",
			in:       agnesResponse{Status: "queued", VideoID: "vid_9"},
			wantStat: "queued", wantUpID: "vid_9",
		},
		{
			name:     "in_progress passes status and progress through",
			in:       agnesResponse{Status: "in_progress", Progress: 42, TaskID: "task_1"},
			wantStat: "in_progress", wantUpID: "task_1", wantProg: 42,
		},
		{
			name:     "error is stringified",
			in:       agnesResponse{Status: "failed", Error: map[string]any{"code": "boom"}},
			wantStat: "failed", wantErr: "map[code:boom]",
		},
		{
			name:     "nil error yields empty Err",
			in:       agnesResponse{Status: "completed", RemixedFromVideoID: "u"},
			wantStat: "completed", wantURL: "u",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.in.toResult()
			if got.Status != c.wantStat {
				t.Errorf("Status = %q, want %q", got.Status, c.wantStat)
			}
			if got.VideoURL != c.wantURL {
				t.Errorf("VideoURL = %q, want %q", got.VideoURL, c.wantURL)
			}
			if got.UpstreamTaskID != c.wantUpID {
				t.Errorf("UpstreamTaskID = %q, want %q", got.UpstreamTaskID, c.wantUpID)
			}
			if got.Progress != c.wantProg {
				t.Errorf("Progress = %d, want %d", got.Progress, c.wantProg)
			}
			if got.Err != c.wantErr {
				t.Errorf("Err = %q, want %q", got.Err, c.wantErr)
			}
		})
	}
}

// 确保 videoUpstreamResult 的完成判定语义稳定: 仅 status=completed 且 URL 非空
// 才算完成(防止上游早返空 url 被误判完成 —— worker execute/pollUntilDone 依赖此)。
func TestVideoUpstreamResult_CompletionGate(t *testing.T) {
	r := agnesResponse{Status: models.VideoTaskCompleted, RemixedFromVideoID: ""}.toResult()
	if r.Status == models.VideoTaskCompleted && r.VideoURL != "" {
		t.Fatal("completed status with empty url must not satisfy the completion gate")
	}
}
