package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"autogateway/internal/version"

	"github.com/sirupsen/logrus"
)

// UpgradeRequest 是写入信号文件的载荷.
//
// 宿主机 watcher (systemd service / 伴随容器) 监听信号文件存在与否, 读到后
// 执行 docker compose pull && up -d, 然后删除信号文件.
type UpgradeRequest struct {
	TargetVersion string    `json:"target_version"`
	RequestedBy   string    `json:"requested_by"` // peer_id 或 "self"
	RequestedAt   time.Time `json:"requested_at"`
}

// UpgradeStatus 反映本端最近的升级请求状态. 给 UI 读, 决定是否显示
// "等待 watcher 接管" / "watcher 未部署" 等提示.
type UpgradeStatus struct {
	Pending     bool       `json:"pending"`
	Request     *UpgradeRequest `json:"request,omitempty"`
	WaitingSecs int        `json:"waiting_secs,omitempty"`
}

// 默认信号文件路径; 通过 ENV 覆盖 (测试或自定义部署).
const defaultSignalPath = "/app/data/.upgrade-request"

// semverRegex 不接受 prerelease/build metadata, 只接受规范的 vX.Y.Z 形式.
var semverRegex = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// UpgradeService 负责升级请求的合法性校验与信号文件写入. 进程**不会**自己执行
// docker 命令——主容器零特权, 真正的执行交给宿主机 watcher.
type UpgradeService struct {
	mu         sync.Mutex
	signalPath string
}

// NewUpgradeService 通过 dig 注入. 路径优先取 ENV AUTOGATEWAY_UPGRADE_SIGNAL_PATH,
// 否则用默认 /app/data/.upgrade-request.
func NewUpgradeService() *UpgradeService {
	p := os.Getenv("AUTOGATEWAY_UPGRADE_SIGNAL_PATH")
	if p == "" {
		p = defaultSignalPath
	}
	return &UpgradeService{signalPath: p}
}

// RequestUpgrade 校验请求合法性后写信号文件.
//
// 安全限制:
//   - target_version 必须是合法 semver (vX.Y.Z)
//   - 不允许降级 (target <= current 拒绝)
//   - 已有 pending 升级时拒绝 (防 DoS)
func (s *UpgradeService) RequestUpgrade(req UpgradeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !semverRegex.MatchString(req.TargetVersion) {
		return errors.New("invalid version format, expect vX.Y.Z")
	}
	if compareSemver(req.TargetVersion, version.Version) <= 0 {
		return fmt.Errorf("downgrade not allowed: target=%s current=%s",
			req.TargetVersion, version.Version)
	}
	if _, err := os.Stat(s.signalPath); err == nil {
		return errors.New("upgrade already pending")
	}

	if req.RequestedAt.IsZero() {
		req.RequestedAt = time.Now()
	}

	// 保证父目录存在 (本地 dev / 测试场景)
	if err := os.MkdirAll(filepath.Dir(s.signalPath), 0o755); err != nil {
		return fmt.Errorf("create signal dir: %w", err)
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	if err := os.WriteFile(s.signalPath, payload, 0o600); err != nil {
		return fmt.Errorf("write signal file: %w", err)
	}
	logrus.Infof("upgrade request written: target=%s by=%s path=%s",
		req.TargetVersion, req.RequestedBy, s.signalPath)
	return nil
}

// Status 返回当前的升级请求状态. 若信号文件还在 → pending=true + 当前请求详情;
// 若不存在 → pending=false (说明 watcher 已消费或从未请求).
func (s *UpgradeService) Status() UpgradeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.signalPath)
	if err != nil {
		return UpgradeStatus{Pending: false}
	}
	var req UpgradeRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return UpgradeStatus{Pending: true}
	}
	waiting := int(time.Since(req.RequestedAt).Seconds())
	if waiting < 0 {
		waiting = 0
	}
	return UpgradeStatus{
		Pending:     true,
		Request:     &req,
		WaitingSecs: waiting,
	}
}

// compareSemver 返回 a vs b 的比较结果: 负=小, 0=相等, 正=大.
// 仅支持 v?X.Y.Z 形式, 输入非法时退化为字典序比较.
func compareSemver(a, b string) int {
	pa := parseSemverParts(a)
	pb := parseSemverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] - pb[i]
		}
	}
	return 0
}

func parseSemverParts(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n := 0
		for _, ch := range parts[i] {
			if ch < '0' || ch > '9' {
				n = -1
				break
			}
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}
