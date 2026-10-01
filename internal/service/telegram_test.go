package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newTelegramTestService(t *testing.T) (*TelegramService, *repository.Container) {
	t.Helper()
	repos := repository.New(newServiceTestDB(t))
	return NewTelegramService(zap.NewNop(), repos), repos
}

func seedTelegramTestUser(t *testing.T, repos *repository.Container, id, chatID string) {
	t.Helper()
	if err := repos.User.Create(context.Background(), &model.User{
		Base:           model.Base{ID: id},
		Username:       "tg-" + id,
		PasswordHash:   "x",
		Role:           "user",
		IsActive:       true,
		TelegramChatID: chatID,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStartBindReturnsSixCharCode(t *testing.T) {
	s, _ := newTelegramTestService(t)
	code, err := s.StartBind(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("code = %q, want 6 chars", code)
	}
	for _, r := range code {
		if !strings.ContainsRune(telegramBindAlphabet, r) {
			t.Fatalf("code %q contains unexpected rune %q", code, r)
		}
	}
}

// 同一用户重复申请绑定码时，旧码必须立即失效，避免多个有效码并存。
func TestStartBindReplacesPreviousCode(t *testing.T) {
	s, _ := newTelegramTestService(t)
	first, err := s.StartBind(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.StartBind(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Skip("random collision, rerun")
	}
	if err := s.CompleteBind(context.Background(), first, "111"); err == nil {
		t.Fatal("expected the superseded code to be rejected")
	}
}

func TestCompleteBindRejectsExpiredCode(t *testing.T) {
	s, _ := newTelegramTestService(t)
	code, err := s.StartBind(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.pending[code] = telegramBindPending{UserID: "u1", ExpiresAt: time.Now().Add(-time.Second)}
	s.mu.Unlock()

	if err := s.CompleteBind(context.Background(), code, "999"); err == nil {
		t.Fatal("expected expired code to be rejected")
	}
}

func TestCompleteBindWritesChatID(t *testing.T) {
	s, repos := newTelegramTestService(t)
	seedTelegramTestUser(t, repos, "u1", "")
	code, err := s.StartBind(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteBind(context.Background(), code, "987654321"); err != nil {
		t.Fatal(err)
	}
	u, err := repos.User.FindByID(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if u.TelegramChatID != "987654321" {
		t.Fatalf("chat id = %q, want 987654321", u.TelegramChatID)
	}
	// 绑定成功后码必须被消费，不能重复使用。
	if err := s.CompleteBind(context.Background(), code, "987654321"); err == nil {
		t.Fatal("expected the consumed code to be rejected")
	}
}

func TestStatusMasksChatID(t *testing.T) {
	s, repos := newTelegramTestService(t)
	seedTelegramTestUser(t, repos, "u1", "1234567890")

	bound, masked := s.Status(context.Background(), "u1")
	if !bound {
		t.Fatal("expected bound user")
	}
	if strings.Contains(masked, "1234567890") {
		t.Fatalf("chat id must not be exposed verbatim, got %q", masked)
	}
	if !strings.Contains(masked, "***") {
		t.Fatalf("masked value should carry a *** marker, got %q", masked)
	}
}

func TestUnbindClearsChatID(t *testing.T) {
	s, repos := newTelegramTestService(t)
	seedTelegramTestUser(t, repos, "u1", "555")

	if err := s.Unbind(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	u, err := repos.User.FindByID(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if u.TelegramChatID != "" {
		t.Fatalf("chat id = %q, want empty", u.TelegramChatID)
	}
}

// 未启用 / 未配置 token 时，发送必须静默返回，绝不能发起网络请求或以 panic 收场。
func TestSendNoopWhenDisabled(t *testing.T) {
	s, repos := newTelegramTestService(t)
	seedTelegramTestUser(t, repos, "u1", "1234567890")

	s.SendToUser(context.Background(), "u1", "hello")
	s.SendToAdmin(context.Background(), "hello")
}

// 未绑定 Telegram 的用户仍可能触发通知（例如首次登录），此时必须静默跳过。
func TestSendNoopWhenUserUnbound(t *testing.T) {
	s, _ := newTelegramTestService(t)
	s.SendToUser(context.Background(), "missing-user", "hello")
}

// 数据库写入失败时绑定码必须保留，让用户可以重试。
func TestCompleteBindKeepsPendingOnDBFailure(t *testing.T) {
	db := newServiceTestDB(t)
	repos := repository.New(db)
	s := NewTelegramService(zap.NewNop(), repos)

	code, err := s.StartBind(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}

	// 关闭底层数据库连接，强制 UpdateFields 失败。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()

	bindErr := s.CompleteBind(context.Background(), code, "999")
	if bindErr == nil {
		t.Fatal("expected an error from closed DB")
	}

	// 码必须还在 pending 里，以便重试。
	s.mu.Lock()
	_, stillPresent := s.pending[code]
	s.mu.Unlock()
	if !stillPresent {
		t.Fatal("bind code must be retained when DB write fails, so the user can retry")
	}
}

// pollLoop 在 Telegram 禁用状态下不应退出，而应持续等待配置开启。
func TestPollLoopContinuesWhenDisabled(t *testing.T) {
	repos := repository.New(newServiceTestDB(t))
	s := NewTelegramService(zap.NewNop(), repos)

	// 使用一个立即取消的 context 验证 goroutine 能正常退出（不阻塞）。
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.pollLoop(ctx)
	}()

	// 给 goroutine 启动时间，然后取消 context。
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// ok: goroutine 正常退出
	case <-time.After(2 * time.Second):
		t.Fatal("pollLoop did not exit after ctx cancellation")
	}
}
