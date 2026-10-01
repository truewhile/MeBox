package service

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newDeviceServiceForTest(t *testing.T) (*DeviceService, *repository.Container, string) {
	t.Helper()
	repos := repository.New(newServiceTestDB(t))
	const userID = "user-1"
	if err := repos.User.Create(context.Background(), &model.User{
		Base:         model.Base{ID: userID},
		Username:     "tester",
		PasswordHash: "x",
		Role:         "user",
		IsActive:     true,
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewDeviceService(zap.NewNop(), repos)
	svc.SetSessionTracker(NewSessionTrackerService(zap.NewNop()))
	return svc, repos, userID
}

// 新终端首次登录必须通知用户，否则「谁在用我的账号」永远无从察觉。
func TestRecordLoginNotifiesOnNewDevice(t *testing.T) {
	svc, _, userID := newDeviceServiceForTest(t)

	type call struct{ userID, text string }
	var calls []call
	svc.SetNotifier(func(_ context.Context, uid, text string) {
		calls = append(calls, call{uid, text})
	})

	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone", "Infuse", "1.2.3.4")

	if len(calls) != 1 {
		t.Fatalf("notifier calls = %d, want 1", len(calls))
	}
	if calls[0].userID != userID {
		t.Fatalf("notifier user = %q, want %q", calls[0].userID, userID)
	}
	if !strings.Contains(calls[0].text, "新设备") {
		t.Fatalf("notifier text = %q, want it to mention 新设备", calls[0].text)
	}
}

// 已知终端重复登录不应刷屏：只在首次建档时通知。
func TestRecordLoginDoesNotNotifyOnKnownDevice(t *testing.T) {
	svc, _, userID := newDeviceServiceForTest(t)

	var count int
	svc.SetNotifier(func(context.Context, string, string) { count++ })

	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone", "Infuse", "1.2.3.4")
	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone", "Infuse", "1.2.3.4")

	if count != 1 {
		t.Fatalf("notifier calls = %d, want exactly 1", count)
	}
}

// 一键踢下线后必须告知用户，否则只会表现为「播放莫名失败」。
func TestKickDeviceNotifiesUser(t *testing.T) {
	svc, _, userID := newDeviceServiceForTest(t)
	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone", "Infuse", "1.2.3.4")

	var kickText string
	svc.SetNotifier(func(_ context.Context, _, text string) { kickText = text })

	if err := svc.KickAllDevices(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
	if kickText == "" {
		t.Fatal("expected a notification after kicking devices")
	}
	if !strings.Contains(kickText, "已下线") && !strings.Contains(kickText, "踢") {
		t.Fatalf("kick notification text = %q, want it to describe the kick", kickText)
	}
}

// 未接线 notifier 时（例如测试环境或 Bot 未配置），所有路径必须保持可用。
func TestDeviceServiceWorksWithoutNotifier(t *testing.T) {
	svc, _, userID := newDeviceServiceForTest(t)
	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone", "Infuse", "1.2.3.4")
	if err := svc.KickAllDevices(context.Background(), userID); err != nil {
		t.Fatal(err)
	}
}

// 设备指纹警告除通知用户外，管理员也必须同步收到告警。
func TestFingerprintWarnNotifiesAdmin(t *testing.T) {
	svc, repos, userID := newDeviceServiceForTest(t)

	// 启用防共享策略
	if err := repos.Setting.Set(context.Background(), SettingAntiShareEnabled, "true"); err != nil {
		t.Fatal(err)
	}

	type call struct{ text string }
	var adminCalls []call
	svc.SetAdminNotifier(func(_ context.Context, text string) {
		adminCalls = append(adminCalls, call{text})
	})
	svc.SetNotifier(func(context.Context, string, string) {}) // 用户通知静默接收

	// 第一次登录注册设备
	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone-A", "Infuse", "1.2.3.4")
	// 同设备 ID 换设备名 → 触发指纹变更警告
	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone-B", "Infuse", "1.2.3.4")

	if len(adminCalls) == 0 {
		t.Fatal("admin must be notified on fingerprint warn")
	}
}

// 账号因设备策略被禁用时，管理员也必须收到告警。
func TestPolicyDisableNotifiesAdmin(t *testing.T) {
	svc, repos, userID := newDeviceServiceForTest(t)

	// 启用防共享策略，设置最大并发客户端为 1
	for _, kv := range [][2]string{
		{SettingAntiShareEnabled, "true"},
		{SettingMaxLoggedClients, "1"},
		{SettingClientActiveDays, "30"},
	} {
		if err := repos.Setting.Set(context.Background(), kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	var adminCalls []string
	svc.SetAdminNotifier(func(_ context.Context, text string) {
		adminCalls = append(adminCalls, text)
	})
	svc.SetNotifier(func(context.Context, string, string) {})

	// 两台不同设备登录，超出上限 → 触发禁用
	svc.RecordLogin(context.Background(), userID, "dev-1", "Phone", "Infuse", "1.2.3.4")
	svc.RecordLogin(context.Background(), userID, "dev-2", "TV", "Emby", "1.2.3.5")

	if len(adminCalls) == 0 {
		t.Fatal("admin must be notified when account is disabled by policy")
	}
	if !strings.Contains(adminCalls[0], "禁用") {
		t.Fatalf("admin notification = %q, want it to mention 禁用", adminCalls[0])
	}
}
