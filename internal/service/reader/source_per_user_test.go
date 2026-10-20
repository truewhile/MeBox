package reader

import (
	"strings"
	"testing"
)

// 书源按用户独立：同一 URL 的两个用户各自持有副本，启停/删除/搜索互不影响。

func TestSourcesArePerUser(t *testing.T) {
	srv := newCacheTestServer()
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	raw := cacheTestSourceJSON(srv.URL)
	if _, err := svc.ImportSources(ctx, "u1", raw); err != nil {
		t.Fatalf("u1 导入失败: %v", err)
	}
	// 第二个用户导入同一书源 URL：旧实现会撞 source_url 唯一索引（导入失败）。
	if _, err := svc.ImportSources(ctx, "u2", raw); err != nil {
		t.Fatalf("u2 导入同一书源失败（书源未按用户隔离）: %v", err)
	}

	u1Sources, err := svc.ListSources(ctx, "u1")
	if err != nil || len(u1Sources) != 1 {
		t.Fatalf("u1 应有 1 个书源: n=%d err=%v", len(u1Sources), err)
	}
	u2Sources, err := svc.ListSources(ctx, "u2")
	if err != nil || len(u2Sources) != 1 {
		t.Fatalf("u2 应有 1 个书源: n=%d err=%v", len(u2Sources), err)
	}
	if u1Sources[0].ID == u2Sources[0].ID {
		t.Fatal("两个用户应各持一份副本（行 ID 不同）")
	}
	if u1Sources[0].UserID != "u1" || u2Sources[0].UserID != "u2" {
		t.Fatalf("书源归属错误: u1=%q u2=%q", u1Sources[0].UserID, u2Sources[0].UserID)
	}

	// 启停互不影响。
	if err := svc.UpdateSourceEnabled(ctx, "u1", u1Sources[0].ID, false); err != nil {
		t.Fatalf("u1 停用失败: %v", err)
	}
	u2After, _ := svc.ListSources(ctx, "u2")
	if len(u2After) != 1 || !u2After[0].Enabled {
		t.Fatal("u1 停用影响到了 u2 的书源")
	}

	// 删除只删自己那份。
	if err := svc.DeleteSource(ctx, "u1", u1Sources[0].ID); err != nil {
		t.Fatalf("u1 删除失败: %v", err)
	}
	if left, _ := svc.ListSources(ctx, "u1"); len(left) != 0 {
		t.Fatalf("u1 应已无书源: n=%d", len(left))
	}
	if left, _ := svc.ListSources(ctx, "u2"); len(left) != 1 {
		t.Fatalf("u2 的书源被误删: n=%d", len(left))
	}

	// 越权删除他人书源必须失败。
	if err := svc.DeleteSource(ctx, "u1", u2Sources[0].ID); err == nil {
		t.Fatal("u1 不应能删除 u2 的书源")
	}
}

// 搜索只使用当前用户的书源范围。
func TestSearchUsesOwnSourcesOnly(t *testing.T) {
	srv := e2eServer()
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	if _, err := svc.ImportSources(ctx, "u1", e2eSourceJSON(srv.URL)); err != nil {
		t.Fatal(err)
	}
	// u2 没有任何书源：搜索应直接报「没有已启用的书源」，而不是用 u1 的源。
	if _, _, err := svc.Search(ctx, "u2", "书", nil, 1); err == nil || !strings.Contains(err.Error(), "没有已启用的书源") {
		t.Fatalf("u2 无书源时应拒绝搜索，实际 err=%v", err)
	}
	// u1 自己的搜索正常。
	books, _, err := svc.Search(ctx, "u1", "书", nil, 1)
	if err != nil {
		t.Fatalf("u1 搜索失败: %v", err)
	}
	if len(books) == 0 {
		t.Fatal("u1 应搜到结果")
	}
}

// 同一书源 URL 的两个用户副本：删除再导入仍然可用（物理删除释放唯一键）。
func TestDeleteAndReimportSameSource(t *testing.T) {
	srv := newCacheTestServer()
	defer srv.Close()
	svc, _ := newLoginTestService(t)
	ctx := t.Context()

	if _, err := svc.ImportSources(ctx, "u1", cacheTestSourceJSON(srv.URL)); err != nil {
		t.Fatal(err)
	}
	sources, _ := svc.ListSources(ctx, "u1")
	if err := svc.DeleteSource(ctx, "u1", sources[0].ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := svc.ImportSources(ctx, "u1", cacheTestSourceJSON(srv.URL)); err != nil {
		t.Fatalf("删除后重新导入失败: %v", err)
	}
	if again, _ := svc.ListSources(ctx, "u1"); len(again) != 1 {
		t.Fatalf("重新导入后应有 1 个书源: n=%d", len(again))
	}
}
