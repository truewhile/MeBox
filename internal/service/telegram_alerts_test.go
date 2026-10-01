package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// 任务失败必须通知管理员：否则只会停留在任务队列里等人自己发现。
func TestTaskFailureNotifiesAdmin(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	tracker := NewTaskTrackerService(zap.NewNop(), nil)

	type call struct{ text string }
	var adminCalls []call
	tracker.SetFailureNotifier(func(_ context.Context, text string) {
		adminCalls = append(adminCalls, call{text})
	})

	h := tracker.Start(TaskKindOrganize, "自动整理", TaskUpdate{})
	h.Finish(errors.New("disk full"), TaskUpdate{})

	if len(adminCalls) != 1 {
		t.Fatalf("admin notifications = %d, want 1", len(adminCalls))
	}
	if !strings.Contains(adminCalls[0].text, "自动整理") {
		t.Fatalf("notification = %q, want it to name the task", adminCalls[0].text)
	}
	if !strings.Contains(adminCalls[0].text, "disk full") {
		t.Fatalf("notification = %q, want it to carry the error", adminCalls[0].text)
	}
	_ = repos
}

// 动态字段须 HTML 转义，避免路径/错误里的 <>& 破坏 parse_mode。
func TestTaskFailureAlertEscapesHTML(t *testing.T) {
	got := formatTaskFailureAlert(BackgroundTask{
		Name:       "整理 <script>",
		SourcePath: "C:\\a&b>c",
		Error:      "fail <b>now</b>",
	})
	for _, bad := range []string{"<script>", "a&b>c", "<b>now</b>"} {
		if strings.Contains(got, bad) {
			t.Fatalf("alert still contains raw %q: %s", bad, got)
		}
	}
	for _, want := range []string{"整理 &lt;script&gt;", "a&amp;b&gt;c", "fail &lt;b&gt;now&lt;/b&gt;"} {
		if !strings.Contains(got, want) {
			t.Fatalf("alert missing escaped %q: %s", want, got)
		}
	}
}

// 成功结束的任务不应触发失败通知。
func TestTaskSuccessDoesNotNotifyAdmin(t *testing.T) {
	tracker := NewTaskTrackerService(zap.NewNop(), nil)
	var count int
	tracker.SetFailureNotifier(func(context.Context, string) { count++ })

	h := tracker.Start(TaskKindOrganize, "自动整理", TaskUpdate{})
	h.Finish(nil, TaskUpdate{})

	if count != 0 {
		t.Fatalf("admin notifications = %d, want 0", count)
	}
}

// 未接线通知时，任务路径必须照常完成。
func TestTaskTrackerWorksWithoutNotifier(t *testing.T) {
	tracker := NewTaskTrackerService(zap.NewNop(), nil)
	h := tracker.Start(TaskKindScan, "扫描", TaskUpdate{})
	h.Finish(errors.New("boom"), TaskUpdate{})
}

func TestExpiryWarningsTargetDueUsersOnly(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	ctx := context.Background()

	// 48h 内到期（落在 "3d" 桶：1-3 天）→ 应该提醒
	soon := now.Add(48 * time.Hour)
	// 三十天后到期 → 不应提醒
	far := now.Add(30 * 24 * time.Hour)
	for _, u := range []*model.User{
		{Base: model.Base{ID: "u-soon"}, Username: "soon", PasswordHash: "x", Role: "user", IsActive: true, ExpiredAt: &soon, TelegramChatID: "111"},
		{Base: model.Base{ID: "u-far"}, Username: "far", PasswordHash: "x", Role: "user", IsActive: true, ExpiredAt: &far, TelegramChatID: "222"},
		{Base: model.Base{ID: "u-never"}, Username: "never", PasswordHash: "x", Role: "user", IsActive: true, TelegramChatID: "333"},
	} {
		if err := repos.User.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewTelegramExpiryWatcher(zap.NewNop(), repos)
	svc.now = func() time.Time { return now }

	var notified []string
	svc.SetUserNotifier(func(_ context.Context, userID, _ string) { notified = append(notified, userID) })

	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 1 || notified[0] != "u-soon" {
		t.Fatalf("notified = %v, want [u-soon]", notified)
	}

	// 同一桶重复运行不得重复打扰。
	notified = nil
	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 0 {
		t.Fatalf("second run notified %v, want none", notified)
	}
}

// 未绑定 Telegram 的到期用户不应被通知，且不应写入标记键。
func TestExpirySkipsUnboundUsers(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	ctx := context.Background()

	soon := now.Add(48 * time.Hour)
	// 未绑定（TelegramChatID 为空）
	unbound := &model.User{Base: model.Base{ID: "u-unbound"}, Username: "unbound", PasswordHash: "x", Role: "user", IsActive: true, ExpiredAt: &soon}
	// 已绑定
	bound := &model.User{Base: model.Base{ID: "u-bound"}, Username: "bound", PasswordHash: "x", Role: "user", IsActive: true, ExpiredAt: &soon, TelegramChatID: "999"}
	for _, u := range []*model.User{unbound, bound} {
		if err := repos.User.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewTelegramExpiryWatcher(zap.NewNop(), repos)
	svc.now = func() time.Time { return now }

	var notified []string
	svc.SetUserNotifier(func(_ context.Context, userID, _ string) { notified = append(notified, userID) })

	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range notified {
		if id == "u-unbound" {
			t.Fatal("unbound user must not be notified")
		}
	}
	found := false
	for _, id := range notified {
		if id == "u-bound" {
			found = true
		}
	}
	if !found {
		t.Fatal("bound user must be notified")
	}

	// 未绑定用户不应写标记键：再次跑时仍然跳过（不重复通知）。
	notified = nil
	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range notified {
		if id == "u-unbound" {
			t.Fatal("unbound user notified on second run")
		}
	}
}

// 两个提醒桶（3d / 1d）各触发一次，互不干扰。
func TestExpiryTwoBuckets(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	ctx := context.Background()

	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.Local)
	// 用户的到期时间在 "1d" 桶内
	oneDay := now.Add(12 * time.Hour)
	u := &model.User{Base: model.Base{ID: "u1"}, Username: "alice", PasswordHash: "x", Role: "user", IsActive: true, ExpiredAt: &oneDay, TelegramChatID: "555"}
	if err := repos.User.Create(ctx, u); err != nil {
		t.Fatal(err)
	}

	svc := NewTelegramExpiryWatcher(zap.NewNop(), repos)
	svc.now = func() time.Time { return now }

	var count int
	svc.SetUserNotifier(func(context.Context, string, string) { count++ })

	// 第一次运行：1d 桶触发
	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("first run: count = %d, want 1", count)
	}

	// 第二次运行：1d 桶已标记，不再触发
	if err := svc.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("second run: count = %d, want still 1", count)
	}
}
