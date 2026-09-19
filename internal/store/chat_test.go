package store

import (
	"testing"
)

// TestChatHistory 对话历史：插入/按会话查询（升序）/清空/会话列表。
func TestChatHistory(t *testing.T) {
	mgr, ctx := openUserTestMeta(t)
	metaDB, dialect := mgr.Meta()

	for i, role := range []string{"user", "assistant", "user"} {
		if err := ChatInsert(ctx, metaDB, dialect, "alice", "s1", role, string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := ChatInsert(ctx, metaDB, dialect, "alice", "s2", "user", "第二个会话"); err != nil {
		t.Fatal(err)
	}
	if err := ChatInsert(ctx, metaDB, dialect, "bob", "s1", "user", "other"); err != nil {
		t.Fatal(err)
	}

	// 按会话过滤
	list, err := ChatList(ctx, metaDB, dialect, "alice", "s1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("limit=2 应返回2条, got %d", len(list))
	}
	if list[0].Content != "b" || list[0].Role != "assistant" {
		t.Fatalf("升序回放异常: %+v", list[0])
	}
	all, err := ChatList(ctx, metaDB, dialect, "alice", "s1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].Content != "a" || all[2].Content != "c" {
		t.Fatalf("全量回放应为a/b/c升序: %+v", all)
	}

	// 会话列表:alice 两个会话,s2 最近活跃在前,标题取首条提问
	sessions, err := ChatSessions(ctx, metaDB, dialect, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || sessions[0].SessionID != "s2" {
		t.Fatalf("会话列表异常: %+v", sessions)
	}
	if sessions[0].Title != "第二个会话" || sessions[1].Title != "a" {
		t.Fatalf("会话标题异常: %+v", sessions)
	}
	if sessions[1].Count != 3 {
		t.Fatalf("s1 计数应为3: %+v", sessions[1])
	}

	// 删除单个会话
	if err := ChatClear(ctx, metaDB, dialect, "alice", "s1"); err != nil {
		t.Fatal(err)
	}
	rest, _ := ChatList(ctx, metaDB, dialect, "alice", "s1", 100)
	if len(rest) != 0 {
		t.Fatalf("s1 清空后应为0条, got %d", len(rest))
	}
	keep, _ := ChatList(ctx, metaDB, dialect, "alice", "s2", 100)
	if len(keep) != 1 {
		t.Fatalf("s2 应保留1条, got %d", len(keep))
	}
	// bob 不受影响
	bobList, _ := ChatList(ctx, metaDB, dialect, "bob", "s1", 100)
	if len(bobList) != 1 {
		t.Fatalf("bob 应保留1条, got %d", len(bobList))
	}
}
