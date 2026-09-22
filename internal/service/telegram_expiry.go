package service

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// expiryNotifiedKeyPrefix 记录「某个用户在某个提醒桶已经提醒过」，防止重复打扰。
// 键格式：telegram.expiry_notified.{userID}.{bucket}，bucket 为 "3d" 或 "1d"。
const expiryNotifiedKeyPrefix = "telegram.expiry_notified."

// expiryWarnWindow 是「即将到期」的判定总窗口（所有提醒桶中最大的上限）。
const expiryWarnWindow = 72 * time.Hour

// expiryBucket 描述一个提醒时间窗口。
type expiryBucket struct {
	name string
	from time.Duration // 剩余时间下限（不含）
	to   time.Duration // 剩余时间上限（含）
}

// expiryBuckets 定义两次提醒：约 3 天前 / 约 1 天前，各提醒一次。
var expiryBuckets = []expiryBucket{
	{"3d", 24 * time.Hour, 72 * time.Hour}, // 1–3 天
	{"1d", 0, 24 * time.Hour},              // 0–1 天
}

// TelegramExpiryWatcher 每日巡检即将到期的账号并提醒用户。
//
// 只负责「发现 + 通知 + 去重」，发送本身交给注入的 notifier，因此没有配置
// Telegram 时整条链路静默跳过。
type TelegramExpiryWatcher struct {
	log  *zap.Logger
	repo *repository.Container
	now  func() time.Time

	userNotifier func(ctx context.Context, userID, text string)
}

// NewTelegramExpiryWatcher 构建巡检器。
func NewTelegramExpiryWatcher(log *zap.Logger, repo *repository.Container) *TelegramExpiryWatcher {
	return &TelegramExpiryWatcher{log: log, repo: repo, now: time.Now}
}

// SetUserNotifier 注入用户通知回调（通常是 TelegramService.SendToUser）。
func (w *TelegramExpiryWatcher) SetUserNotifier(fn func(ctx context.Context, userID, text string)) {
	if w == nil {
		return
	}
	w.userNotifier = fn
}

// RunOnce 执行一次巡检。每个用户每个提醒桶（"3d" / "1d"）最多触发一次，
// 未绑定 Telegram 的用户跳过且不写标记键。
func (w *TelegramExpiryWatcher) RunOnce(ctx context.Context) error {
	if w == nil || w.repo == nil || w.repo.User == nil || w.userNotifier == nil {
		return nil
	}
	now := w.now()

	for _, bucket := range expiryBuckets {
		from := now.Add(bucket.from) // expired_at > from
		to := now.Add(bucket.to)    // expired_at <= to

		users, err := w.dueUsers(ctx, from, to)
		if err != nil {
			return err
		}
		for _, u := range users {
			if u.ExpiredAt == nil {
				continue
			}
			// 跳过未绑定 Telegram 的用户，且不消耗标记键。
			if strings.TrimSpace(u.TelegramChatID) == "" {
				continue
			}
			markKey := expiryNotifiedKeyPrefix + u.ID + "." + bucket.name
			if seen, err := w.repo.Setting.Get(ctx, markKey); err == nil && strings.TrimSpace(seen) != "" {
				continue
			}
			w.userNotifier(ctx, u.ID, fmt.Sprintf(
				"⏳ 账号 <b>%s</b> 将于 %s 到期，请及时续期，避免到期后无法登录。",
				html.EscapeString(u.Username), u.ExpiredAt.In(time.Local).Format("2006-01-02 15:04"),
			))
			// 仅在发送（尝试）后才写标记键。
			if err := w.repo.Setting.Set(ctx, markKey, "1"); err != nil && w.log != nil {
				w.log.Warn("telegram expiry mark failed", zap.String("user_id", u.ID), zap.Error(err))
			}
		}
	}
	return nil
}

// dueUsers 返回 (now, deadline] 内到期且仍处于启用状态的账号。
func (w *TelegramExpiryWatcher) dueUsers(ctx context.Context, now, deadline time.Time) ([]*model.User, error) {
	var users []model.User
	err := w.repo.DB.WithContext(ctx).
		Where("expired_at IS NOT NULL AND expired_at > ? AND expired_at <= ?", now, deadline).
		Where("is_active = ?", true).
		Find(&users).Error
	if err != nil {
		return nil, err
	}
	out := make([]*model.User, 0, len(users))
	for i := range users {
		out = append(out, &users[i])
	}
	return out, nil
}
