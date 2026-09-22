package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/repository"
)

// Telegram 通知相关的设置键。全部存在 settings 表，由管理员在「设备与通知」
// 设置分组里维护；带安全默认值：未启用时所有发送静默跳过。
const (
	SettingTelegramEnabled     = "telegram.enabled"      // 总开关（默认关）
	SettingTelegramBotToken    = "telegram.bot_token"    // Bot API Token
	SettingTelegramAdminChatID = "telegram.admin_chat_id" // 管理员接收运维通知的会话 ID
)

// telegramBindAlphabet 去掉容易看错的 0/O/1/I，降低手抄绑定码时的出错率。
const telegramBindAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const (
	// telegramBindTTL 是绑定码的有效期。到期后必须重新生成。
	telegramBindTTL = 5 * time.Minute
	// telegramAPITimeout 覆盖单次 sendMessage 请求。
	telegramAPITimeout = 10 * time.Second
	// telegramPollClientTimeout 是长轮询专用 HTTP 客户端的超时，必须 > telegramPollTimeout。
	telegramPollClientTimeout = 35 * time.Second
	// telegramPollTimeout 是 getUpdates 的长轮询等待秒数。
	telegramPollTimeout = 30
	// telegramPollBackoff 是长轮询失败后的重试间隔，避免打爆 API。
	telegramPollBackoff = 5 * time.Second
	// telegramDisabledCheckInterval 是禁用状态下检查配置变化的间隔。
	telegramDisabledCheckInterval = 10 * time.Second
)

var (
	// ErrTelegramBindInvalid 表示绑定码不存在（不存在、已被使用，或已被新码取代）。
	ErrTelegramBindInvalid = errors.New("telegram bind code not found")
	// ErrTelegramBindExpired 表示绑定码已过期。
	ErrTelegramBindExpired = errors.New("telegram bind code expired")
)

// telegramBindPending 记录一个待消费的绑定码。
type telegramBindPending struct {
	UserID    string
	ExpiresAt time.Time
}

// TelegramService 负责把通知发到 Telegram，并提供账号绑定。
//
// 范围被刻意收窄：Bot 只处理 /bind 与 /start 两条命令，不做开注、签到、
// 兑换码等命令体系。绑定走「网页生成一次性码 → Bot 端 /bind <code>」，
// 这样服务端不需要用户手工填写 chat id。
type TelegramService struct {
	log  *zap.Logger
	repo *repository.Container

	mu      sync.Mutex
	pending map[string]telegramBindPending // code -> pending
	byUser  map[string]string              // userID -> code（同一用户只保留最新码）

	// apiBase 允许测试指向本地假服务；生产固定为 Telegram 官方地址。
	apiBase string
	// lastUpdateID 是 getUpdates 的增量水位，避免重复处理同一条命令。
	lastUpdateID int64
	// client 可注入，便于测试替换传输层（sendMessage 等短请求）。
	client *http.Client
	// pollClient 专用于 getUpdates 长轮询，超时须 > telegramPollTimeout。
	pollClient *http.Client
}

// NewTelegramService 构建 TelegramService。
func NewTelegramService(log *zap.Logger, repo *repository.Container) *TelegramService {
	return &TelegramService{
		log:        log,
		repo:       repo,
		pending:    make(map[string]telegramBindPending),
		byUser:     make(map[string]string),
		apiBase:    "https://api.telegram.org",
		client:     &http.Client{Timeout: telegramAPITimeout},
		pollClient: &http.Client{Timeout: telegramPollClientTimeout},
	}
}

// Start 在后台运行 Bot 命令长轮询。goroutine 始终启动；
// 未启用或未配置 Token 时 pollLoop 内部休眠等待配置就绪，ctx 结束时停止。
func (s *TelegramService) Start(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	helper.Go(s.log, "service.telegramBotPoll", func() { s.pollLoop(ctx) })
	if s.log != nil {
		s.log.Info("telegram poller started")
	}
}

type telegramConfig struct {
	Enabled  bool
	BotToken string
	AdminID  string
}

func (s *TelegramService) config(ctx context.Context) telegramConfig {
	return telegramConfig{
		Enabled:  s.enabled(ctx),
		BotToken: s.setting(ctx, SettingTelegramBotToken),
		AdminID:  s.setting(ctx, SettingTelegramAdminChatID),
	}
}

func (s *TelegramService) setting(ctx context.Context, key string) string {
	if s == nil || s.repo == nil || s.repo.Setting == nil {
		return ""
	}
	v, err := s.repo.Setting.Get(ctx, key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func (s *TelegramService) enabled(ctx context.Context) bool {
	return parseBoolSetting(s.setting(ctx, SettingTelegramEnabled), false)
}

// SendToUser 把消息发给用户绑定的 Telegram 会话。未绑定、未启用或发送失败
// 都只记日志并返回：通知永远不能影响触发它的业务流程。
func (s *TelegramService) SendToUser(ctx context.Context, userID, htmlText string) {
	if s == nil || s.repo == nil || userID == "" || strings.TrimSpace(htmlText) == "" {
		return
	}
	u, err := s.repo.User.FindByID(ctx, userID)
	if err != nil || u == nil {
		return
	}
	chatID := strings.TrimSpace(u.TelegramChatID)
	if chatID == "" {
		return
	}
	if err := s.sendMessage(ctx, chatID, htmlText); err != nil {
		s.warn("telegram send to user failed", userID, err)
	}
}

// SendToAdmin 把消息发给管理员会话，用于运维类事件（任务失败等）。
func (s *TelegramService) SendToAdmin(ctx context.Context, htmlText string) {
	if s == nil || s.repo == nil || strings.TrimSpace(htmlText) == "" {
		return
	}
	chatID := s.setting(ctx, SettingTelegramAdminChatID)
	if chatID == "" {
		return
	}
	if err := s.sendMessage(ctx, chatID, htmlText); err != nil {
		s.warn("telegram send to admin failed", chatID, err)
	}
}

func (s *TelegramService) warn(msg, target string, err error) {
	if s.log == nil {
		return
	}
	s.log.Warn(msg, zap.String("target", target), zap.Error(err))
}

// Configured 报告通知是否已具备发送条件：启用 + Token + 管理员会话都齐了。
func (s *TelegramService) Configured(ctx context.Context) bool {
	if s == nil {
		return false
	}
	cfg := s.config(ctx)
	return cfg.Enabled && cfg.BotToken != "" && cfg.AdminID != ""
}

// SendToAdminChecked 是需要把失败反馈给管理员的场景（例如设置页的测试按钮）。
// 它配置未就绪时返回错误；普通事件通知仍应使用静默的 SendToAdmin。
func (s *TelegramService) SendToAdminChecked(ctx context.Context, htmlText string) error {
	if s == nil || s.repo == nil {
		return errors.New("telegram service unavailable")
	}
	chatID := s.setting(ctx, SettingTelegramAdminChatID)
	if chatID == "" {
		return errors.New("未配置管理员 Chat ID")
	}
	if !s.enabled(ctx) {
		return errors.New("Telegram 通知未启用")
	}
	if s.setting(ctx, SettingTelegramBotToken) == "" {
		return errors.New("未配置 Bot Token")
	}
	return s.sendMessage(ctx, chatID, htmlText)
}

// sendMessage 是唯一的出网点。所有前置条件在这里统一校验。
func (s *TelegramService) sendMessage(ctx context.Context, chatID, htmlText string) error {
	cfg := s.config(ctx)
	if !cfg.Enabled || cfg.BotToken == "" {
		return nil
	}
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       htmlText,
		"parse_mode": "HTML",
	}
	return s.call(ctx, cfg.BotToken, "sendMessage", payload, nil)
}

// call 发起一次 Bot API 调用（使用短请求客户端）。
func (s *TelegramService) call(ctx context.Context, token, method string, payload map[string]any, out any) error {
	return s.callWithClient(ctx, s.client, token, method, payload, out)
}

// callWithClient 发起一次 Bot API 调用，允许注入自定义 HTTP 客户端（例如长轮询专用）。
// 失败时返回 Telegram 的 description，便于排查是 Token 错误、chat 不存在还是被限流。
func (s *TelegramService) callWithClient(ctx context.Context, client *http.Client, token, method string, payload map[string]any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/bot%s/%s", s.apiBase, token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if client == nil {
		client = &http.Client{Timeout: telegramAPITimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, err)
	}
	if !envelope.OK {
		if envelope.Description == "" {
			envelope.Description = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return fmt.Errorf("telegram %s: %s", method, envelope.Description)
	}
	if out != nil && len(envelope.Result) > 0 {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

// StartBind 生成一次性绑定码。同一用户重复调用时旧码立即失效。
func (s *TelegramService) StartBind(ctx context.Context, userID string) (string, error) {
	if s == nil || strings.TrimSpace(userID) == "" {
		return "", errors.New("telegram bind: empty user id")
	}
	code, err := randomBindCode()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.byUser[userID]; ok {
		delete(s.pending, prev)
	}
	s.pending[code] = telegramBindPending{UserID: userID, ExpiresAt: time.Now().Add(telegramBindTTL)}
	s.byUser[userID] = code
	return code, nil
}

// CompleteBind 消费绑定码并写入用户的 TelegramChatID。
// 只在数据库写入成功后才删除 pending 中的绑定码；失败时码保留以便重试。
func (s *TelegramService) CompleteBind(ctx context.Context, code, chatID string) error {
	if s == nil || s.repo == nil {
		return ErrTelegramBindInvalid
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	chatID = strings.TrimSpace(chatID)
	if code == "" || chatID == "" {
		return ErrTelegramBindInvalid
	}

	// 先读取，不删除——只有在操作成功后才消费绑定码。
	s.mu.Lock()
	pending, ok := s.pending[code]
	s.mu.Unlock()
	if !ok {
		return ErrTelegramBindInvalid
	}

	if time.Now().After(pending.ExpiresAt) {
		// 过期码：删除并返回，不再保留。
		s.mu.Lock()
		delete(s.pending, code)
		if s.byUser[pending.UserID] == code {
			delete(s.byUser, pending.UserID)
		}
		s.mu.Unlock()
		return ErrTelegramBindExpired
	}

	if err := s.repo.User.UpdateFields(ctx, pending.UserID, map[string]any{"telegram_chat_id": chatID}); err != nil {
		// DB 失败时保留 pending，调用方可重试。
		return err
	}

	// 写入成功后再消费绑定码。
	s.mu.Lock()
	delete(s.pending, code)
	if s.byUser[pending.UserID] == code {
		delete(s.byUser, pending.UserID)
	}
	s.mu.Unlock()
	return nil
}

// Unbind 清空用户的 Telegram 绑定。
func (s *TelegramService) Unbind(ctx context.Context, userID string) error {
	if s == nil || s.repo == nil {
		return nil
	}
	s.mu.Lock()
	if code, ok := s.byUser[userID]; ok {
		delete(s.pending, code)
		delete(s.byUser, userID)
	}
	s.mu.Unlock()
	return s.repo.User.UpdateFields(ctx, userID, map[string]any{"telegram_chat_id": ""})
}

// Status 返回绑定状态与脱敏后的会话 ID，供个人资料页展示。
func (s *TelegramService) Status(ctx context.Context, userID string) (bool, string) {
	if s == nil || s.repo == nil || userID == "" {
		return false, ""
	}
	u, err := s.repo.User.FindByID(ctx, userID)
	if err != nil || u == nil || strings.TrimSpace(u.TelegramChatID) == "" {
		return false, ""
	}
	return true, MaskSecret(u.TelegramChatID)
}

// MaskSecret 保留首尾各两个字符，中间以 *** 取代。用于 Token 与会话 ID
// 这类「界面需要辨识、但不应完整下发」的凭据。
func MaskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	r := []rune(value)
	if len(r) <= 4 {
		return "***"
	}
	return string(r[:2]) + "***" + string(r[len(r)-2:])
}

func randomBindCode() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, 6)
	for i, b := range buf {
		out[i] = telegramBindAlphabet[int(b)%len(telegramBindAlphabet)]
	}
	return string(out), nil
}

var telegramBindCommandPattern = regexp.MustCompile(`(?i)^/bind(?:@\w+)?\s+(\S+)\s*$`)

// pollLoop 长轮询 Bot 更新，只处理绑定相关的两条命令。
// 当 Telegram 未启用或 Token 未配置时，休眠后继续循环以便配置变更后自动激活。
func (s *TelegramService) pollLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		cfg := s.config(ctx)
		if !cfg.Enabled || cfg.BotToken == "" {
			if !sleepCtx(ctx, telegramDisabledCheckInterval) {
				return
			}
			continue
		}
		updates, err := s.fetchUpdates(ctx, cfg.BotToken)
		if err != nil {
			if s.log != nil && ctx.Err() == nil {
				s.log.Warn("telegram getUpdates failed", zap.Error(err))
			}
			if !sleepCtx(ctx, telegramPollBackoff) {
				return
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= s.lastUpdateID {
				s.lastUpdateID = u.UpdateID + 1
			}
			s.handleUpdate(ctx, cfg.BotToken, u)
		}
	}
}

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func (s *TelegramService) fetchUpdates(ctx context.Context, token string) ([]telegramUpdate, error) {
	s.mu.Lock()
	offset := s.lastUpdateID
	s.mu.Unlock()

	payload := map[string]any{
		"timeout": telegramPollTimeout,
		// 只取消息更新，避免把频道/回调查询也塞进来。
		"allowed_updates": []string{"message"},
	}
	if offset > 0 {
		payload["offset"] = offset
	}
	var updates []telegramUpdate
	if err := s.callWithClient(ctx, s.pollClient, token, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (s *TelegramService) handleUpdate(ctx context.Context, token string, u telegramUpdate) {
	if u.Message == nil {
		return
	}
	text := strings.TrimSpace(u.Message.Text)
	chatID := fmt.Sprintf("%d", u.Message.Chat.ID)
	if chatID == "0" || text == "" {
		return
	}

	if match := telegramBindCommandPattern.FindStringSubmatch(text); match != nil {
		s.replyBind(ctx, token, chatID, match[1])
		return
	}
	if strings.HasPrefix(strings.ToLower(text), "/start") {
		if err := s.call(ctx, token, "sendMessage", map[string]any{
			"chat_id":    chatID,
			"text":       "MeBox 通知绑定：在网页「个人资料 → Telegram 通知」生成 6 位绑定码，然后发送 <code>/bind 绑定码</code>。",
			"parse_mode": "HTML",
		}, nil); err != nil {
			s.warn("telegram /start reply failed", chatID, err)
		}
	}
}

func (s *TelegramService) replyBind(ctx context.Context, token, chatID, code string) {
	message := ""
	switch err := s.CompleteBind(ctx, code, chatID); {
	case err == nil:
		message = "✅ 绑定成功，之后 MeBox 的账号与设备通知会发到这里。"
	case errors.Is(err, ErrTelegramBindExpired):
		message = "⌛️ 绑定码已过期，请在网页重新生成。"
	case errors.Is(err, ErrTelegramBindInvalid):
		message = "❌ 绑定码无效，请在网页重新生成后再试。"
	default:
		message = "⚠️ 绑定失败，请稍后重试或查看服务日志。"
	}
	if err := s.call(ctx, token, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    message,
	}, nil); err != nil {
		s.warn("telegram bind reply failed", chatID, err)
	}
}

// sleepCtx 等待指定时长，ctx 结束则提前返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
