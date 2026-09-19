package store

import (
	"context"
	"path/filepath"
	"testing"

	"litereport/internal/dbx"
)

func openTestMeta(t *testing.T) (*dbx.Manager, context.Context) {
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

// TestPushSaveKeepsChannelWebhook 回归：编辑推送任务不得丢失 channel/webhook 字段。
func TestPushSaveKeepsChannelWebhook(t *testing.T) {
	mgr, ctx := openTestMeta(t)
	metaDB, dialect := mgr.Meta()

	p := &PushRow{ReportCode: "demo_report", Emails: "a@b.com", IntervalMinutes: 60, Channel: "dingtalk", Webhook: "https://oapi.example.com/robot?k=1"}
	if err := PushSave(ctx, metaDB, dialect, p); err != nil {
		t.Fatal(err)
	}

	list, err := PushList(ctx, metaDB, dialect)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d 条, want 1", len(list))
	}
	if list[0].Channel != "dingtalk" || list[0].Webhook != "https://oapi.example.com/robot?k=1" {
		t.Fatalf("新建后 channel/webhook = %q/%q", list[0].Channel, list[0].Webhook)
	}

	// 编辑：修改收件人，channel/webhook 必须保留
	list[0].Emails = "c@d.com"
	list[0].Channel = "wecom"
	list[0].Webhook = "https://qyapi.example.com/key=2"
	if err := PushSave(ctx, metaDB, dialect, &list[0]); err != nil {
		t.Fatal(err)
	}
	again, _ := PushList(ctx, metaDB, dialect)
	if again[0].Channel != "wecom" || again[0].Webhook != "https://qyapi.example.com/key=2" || again[0].Emails != "c@d.com" {
		t.Fatalf("编辑后 channel/webhook/emails = %q/%q/%q", again[0].Channel, again[0].Webhook, again[0].Emails)
	}
}

// TestPushDueCarriesChannel 调度器扫描结果必须带 channel/webhook（webhook 推送依赖）。
func TestPushDueCarriesChannel(t *testing.T) {
	mgr, ctx := openTestMeta(t)
	metaDB, dialect := mgr.Meta()

	p := &PushRow{ReportCode: "demo_report", IntervalMinutes: 1, Channel: "dingtalk", Webhook: "https://oapi.example.com/robot?k=1"}
	if err := PushSave(ctx, metaDB, dialect, p); err != nil {
		t.Fatal(err)
	}
	due, err := PushDue(ctx, metaDB, dialect, "2026-01-02 15:04:05")
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %d 条, want 1", len(due))
	}
	if due[0].Channel != "dingtalk" || due[0].Webhook != "https://oapi.example.com/robot?k=1" {
		t.Fatalf("due channel/webhook = %q/%q", due[0].Channel, due[0].Webhook)
	}
}

// TestPushMarkRun 回归：执行结果回写 last_run/last_status/last_error。
func TestPushMarkRun(t *testing.T) {
	mgr, ctx := openTestMeta(t)
	metaDB, dialect := mgr.Meta()

	p := &PushRow{ReportCode: "demo_report", IntervalMinutes: 1, Channel: "dingtalk", Webhook: "https://oapi.example.com/robot?k=1"}
	if err := PushSave(ctx, metaDB, dialect, p); err != nil {
		t.Fatal(err)
	}
	longErr := make([]byte, 800)
	for i := range longErr {
		longErr[i] = 'x'
	}
	if err := PushMarkRun(ctx, metaDB, dialect, p.ID, false, string(longErr)); err != nil {
		t.Fatal(err)
	}
	list, _ := PushList(ctx, metaDB, dialect)
	if list[0].LastStatus != "fail" || list[0].LastError == "" || len(list[0].LastError) > 500 {
		t.Fatalf("失败记录: status=%q errLen=%d", list[0].LastStatus, len(list[0].LastError))
	}
	if list[0].LastRun == "" {
		t.Fatal("失败也应推进 last_run（按周期重试，不重扫风暴）")
	}

	if err := PushMarkRun(ctx, metaDB, dialect, p.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	list, _ = PushList(ctx, metaDB, dialect)
	if list[0].LastStatus != "ok" || list[0].LastError != "" {
		t.Fatalf("成功记录: status=%q err=%q", list[0].LastStatus, list[0].LastError)
	}
}
