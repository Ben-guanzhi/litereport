package store

import (
	"context"
	"path/filepath"
	"testing"

	"litereport/internal/dbx"
)

func openUserTestMeta(t *testing.T) (*dbx.Manager, context.Context) {
	t.Helper()
	dir := t.TempDir()
	metaDB, err := dbx.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { metaDB.Close() })
	mgr := dbx.NewManagerWithPool(metaDB, "sqlite", nil, dbx.PoolSpec{})
	ctx := context.Background()
	if err := EnsureTables(metaDB, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExtraTables(ctx, metaDB, "sqlite"); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, metaDB, "sqlite"); err != nil {
		t.Fatal(err)
	}
	return mgr, ctx
}

// TestUserSetPasswordKeepsRole 回归：改密不得丢失角色，token_version 必须递增。
func TestUserSetPasswordKeepsRole(t *testing.T) {
	mgr, ctx := openUserTestMeta(t)
	metaDB, dialect := mgr.Meta()

	// 新建 DB 用户（admin 角色）
	if err := UserSetPasswordTx(ctx, metaDB, dialect, "alice", "hash1", "admin"); err != nil {
		t.Fatal(err)
	}
	// 改密：role 传空表示保留原角色
	if err := UserSetPasswordTx(ctx, metaDB, dialect, "alice", "hash2", ""); err != nil {
		t.Fatal(err)
	}
	h, ver, role, err := UserAuthInfo(ctx, metaDB, dialect, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if h != "hash2" || role != "admin" || ver != 2 {
		t.Fatalf("改密后 hash/ver/role = %q/%d/%q, want hash2/2/admin", h, ver, role)
	}

	// UserSetPassword（改密接口路径）：已有用户保留角色；新用户用 defaultRole
	if err := UserSetPassword(ctx, metaDB, dialect, "alice", "hash3", "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, _, role, _ = UserAuthInfo(ctx, metaDB, dialect, "alice"); role != "admin" {
		t.Fatalf("已有用户改密后 role = %q, want admin（保留原角色）", role)
	}
	if err := UserSetPassword(ctx, metaDB, dialect, "bob", "hashb", "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, _, role, _ = UserAuthInfo(ctx, metaDB, dialect, "bob"); role != "viewer" {
		t.Fatalf("新用户首次改密 role = %q, want viewer（取 defaultRole）", role)
	}
}
