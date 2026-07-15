package services

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestApplySnapshot_RealDBSelfMirrorSmoke 用真实 DB 拷贝做自镜像冒烟: ExportSnapshot 自己
// → ApplySnapshot 自己, 应幂等无 unique 冲突。真实数据含墓碑 / 重复 group / 全局 alias,
// 能抓内存测试库(index 与真实 partial/全局 unique 不同)漏掉的 unique 匹配 bug。
//
// 跑法: sqlite3 data/autogateway.db ".backup /tmp/smoke.db" && \
//       REAL_DB=/tmp/smoke.db go test ./internal/services/ -run SelfMirrorSmoke -v
// 默认 REAL_DB 未设时跳过(不影响 CI)。
func TestApplySnapshot_RealDBSelfMirrorSmoke(t *testing.T) {
	src := os.Getenv("REAL_DB")
	if src == "" {
		t.Skip("set REAL_DB=<autogateway.db copy> to smoke-test self-mirror on real data")
	}
	tmp := t.TempDir() + "/smoke.db"
	copyFileT(t, src, tmp)

	db, err := gorm.Open(sqlite.Open(tmp), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSyncService(db, &mockConfigManager{masterKey: "smoke"}, NewNodeKeypairService(), nil)

	snap, err := s.ExportSnapshot(context.Background())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	t.Logf("snapshot: %d groups, %d keys, %d aliases, %d subgroups, %d settings",
		len(snap.Groups), len(snap.APIKeys), len(snap.ModelAliases), len(snap.SubGroups), len(snap.Settings))

	// 自镜像两次: 用自己导出的活快照镜像自己, 应幂等无 unique 冲突。
	for i := 0; i < 2; i++ {
		if err := s.ApplySnapshot(context.Background(), snap); err != nil {
			t.Fatalf("self-mirror pass %d failed (unique/match bug on real data): %v", i+1, err)
		}
	}
}

func copyFileT(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}
