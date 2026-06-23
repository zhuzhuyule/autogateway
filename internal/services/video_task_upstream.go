package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"autogateway/internal/channel"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/ratelimit"

	"github.com/sirupsen/logrus"
)

// channelUpstream 用现有 ChannelProxy(BuildUpstreamURL/ModifyRequest/GetHTTPClient)
// 向 agnes 发请求,完全复用代理的 URL 构造与鉴权头注入逻辑。
type channelUpstream struct {
	groupManager   *GroupManager
	channelFactory *channel.Factory
	keypool        *keypool.KeyProvider
}

func newChannelUpstream(gm *GroupManager, cf *channel.Factory, kp *keypool.KeyProvider) *channelUpstream {
	return &channelUpstream{groupManager: gm, channelFactory: cf, keypool: kp}
}

// agnesResponse 同时覆盖 POST(create)与 GET(poll)的响应字段。
type agnesResponse struct {
	TaskID             string `json:"task_id"`
	VideoID            string `json:"video_id"`
	Status             string `json:"status"`
	Progress           int    `json:"progress"`
	RemixedFromVideoID string `json:"remixed_from_video_id"`
	Error              any    `json:"error"`
}

func (r agnesResponse) toResult() videoUpstreamResult {
	out := videoUpstreamResult{Status: r.Status, Progress: r.Progress, VideoURL: r.RemixedFromVideoID}
	if r.TaskID != "" {
		out.UpstreamTaskID = r.TaskID
	} else if r.VideoID != "" {
		out.UpstreamTaskID = r.VideoID
	}
	if r.Error != nil {
		out.Err = fmt.Sprintf("%v", r.Error)
	}
	return out
}

func (u *channelUpstream) prepare(group string) (*models.Group, channel.ChannelProxy, *models.APIKey, error) {
	g, err := u.groupManager.GetGroupByName(group)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("group %q not found: %w", group, err)
	}
	ch, err := u.channelFactory.GetChannel(g)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get channel: %w", err)
	}
	key, err := u.keypool.SelectKey(g.ID, ratelimit.Limits{})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("select key: %w", err)
	}
	return g, ch, key, nil
}

// doRequest 构造并发送一个上游请求。proxyPath 形如 "/proxy/<group>/v1/videos"。
// BuildUpstreamURL 会经 stripGatewayPrefix 剥掉 "/proxy/<group>" 前缀,
// 最终落在 <agnes-base>/v1/videos[/<id>]。
func (u *channelUpstream) doRequest(ctx context.Context, group string, method, proxyPath string, body []byte) (*agnesResponse, error) {
	g, ch, key, err := u.prepare(group)
	if err != nil {
		return nil, err
	}
	originalURL := &url.URL{Path: proxyPath}
	target, err := ch.BuildUpstreamURL(originalURL, group)
	if err != nil {
		return nil, fmt.Errorf("build upstream url: %w", err)
	}
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	ch.ModifyRequest(req, key, g)

	resp, err := ch.GetHTTPClient().Do(req)
	if err != nil {
		u.keypool.UpdateStatus(key, g, false, err.Error())
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// I1: 传真实上游错误 body(而非合成的 "status NNN"), 让 keypool 的
		// IsUnCounted/ParseUpstreamError 能识别 429 限流等"不该计入 key 失败"
		// 的错误, 与代理路径(proxy/server.go)行为一致, 避免污染健康 key。
		u.keypool.UpdateStatus(key, g, false, string(raw))
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(raw))
	}
	// M2: 成功不上报(与 proxy 一致, "请求成功不再重置成功次数, 减少 IO 消耗"),
	// 否则每个 poll 周期都触发一次 handleSuccess 的 DB 写。
	var out agnesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode upstream response: %w", err)
	}
	return &out, nil
}

func (u *channelUpstream) Create(ctx context.Context, task *models.VideoTask) (videoUpstreamResult, error) {
	// 组装 body:model + prompt + params(若有)
	payload := map[string]any{"model": task.Model, "prompt": task.Prompt}
	if task.Params != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(task.Params), &extra); err == nil {
			for k, v := range extra {
				payload[k] = v
			}
		} else {
			// M4: 不静默吞掉 —— 坏的 Params 会让视频退化为默认设置, 记一笔便于排查
			logrus.WithError(err).WithField("task", task.ID).
				Warn("video create: invalid task.Params JSON, ignored")
		}
	}
	body, _ := json.Marshal(payload)
	// agnes 的 POST 可能阻塞数分钟,用一个宽松超时的 context
	cctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	resp, err := u.doRequest(cctx, task.GroupName, http.MethodPost,
		"/proxy/"+task.GroupName+"/v1/videos", body)
	if err != nil {
		return videoUpstreamResult{}, err
	}
	logrus.WithField("task", task.ID).Debug("video create upstream returned")
	return resp.toResult(), nil
}

func (u *channelUpstream) Poll(ctx context.Context, task *models.VideoTask) (videoUpstreamResult, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := u.doRequest(cctx, task.GroupName, http.MethodGet,
		"/proxy/"+task.GroupName+"/v1/videos/"+task.UpstreamTaskID, nil)
	if err != nil {
		return videoUpstreamResult{}, err
	}
	return resp.toResult(), nil
}
