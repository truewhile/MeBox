// 115 开放平台 OAuth 授权会话与 Token 维护。
//
// 授权方式（与 QMediaSync 一致）：
//   - built_in_appid：官方应用目录设备码扫码（无回调，轮询 qrcodeapi）
//   - qmediasync / mqfamily：中继授权（浏览器授权后中继 POST 回本服务回调端点，
//     需配置 strm.115_relay_key 共享密钥解密）
//   - moviepilot：MoviePilot 授权服务（轮询其 /u115/token）
//   - clouddrive：CloudDrive 中转（115 授权页 → zhenyunpan 换 token 后回跳）
//
// 授权成功后将 access_token / refresh_token 写入网盘账号配置（加密存储）。
package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service/cloud115"
)

// strm115RelayKeySetting 是中继授权共享 AES 密钥的设置键。
const Strm115RelayKeySetting = "strm.115_relay_key"

const strm115AuthSessionTTL = 6 * time.Minute

// strm115AuthSession 是一次 115 授权会话（内存态，重启后需重新授权）。
type strm115AuthSession struct {
	ID        string
	AccountID string
	Source    cloud115.Source
	Mode      string // qrcode / poll / callback
	QrCode    *cloud115.QrCodeDataReturn
	State     string
	Provider  cloud115.OAuthProvider
	TokenCh   chan *cloud115.OAuthTokenResult
	CreatedAt time.Time
}

// Strm115AuthStartResult 是授权发起结果。
type Strm115AuthStartResult struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"` // qrcode / url
	AuthURL   string `json:"auth_url,omitempty"`
	State     string `json:"state,omitempty"`
	ExpiresIn int64  `json:"expires_in,omitempty"`
	QRCode    *struct {
		UID    string `json:"uid"`
		Time   int64  `json:"time"`
		Sign   string `json:"sign"`
		Qrcode string `json:"qrcode"`
	} `json:"qrcode,omitempty"`
}

// Strm115AuthStatus 是授权轮询结果。
type Strm115AuthStatus struct {
	Status string `json:"status"` // waiting / scanned / confirmed / expired
	Tip    string `json:"tip"`
}

// List115Sources 返回可用的 115 授权来源列表。
func (s *StrmService) List115Sources(ctx context.Context) (map[string][]cloud115.Source, error) {
	out := map[string][]cloud115.Source{}
	out["built_in"] = cloud115.BuiltInAppIDSources()
	out["relay"] = cloud115.BuiltInRelaySources()
	out["third_party"] = cloud115.ThirdPartySources()
	return out, nil
}

// Start115OAuth 发起 115 授权；redirectURL 为空时使用服务自身地址。
func (s *StrmService) Start115OAuth(ctx context.Context, accountID, authSource, appID, provider, redirectURL string) (*Strm115AuthStartResult, error) {
	acct, err := s.repo.StrmAccount.FindByID(ctx, accountID)
	if err != nil || acct == nil {
		return nil, errNotFoundOr(err, "网盘账号不存在")
	}
	if acct.Provider != model.StrmProvider115 {
		return nil, errors.New("该账号不是 115 网盘账号")
	}
	source, err := resolve115Source(authSource, provider, appID)
	if err != nil {
		return nil, err
	}
	sessionID := newStrmID()
	session := &strm115AuthSession{
		ID:        sessionID,
		AccountID: accountID,
		Source:    source,
		TokenCh:   make(chan *cloud115.OAuthTokenResult, 1),
		CreatedAt: time.Now(),
	}

	result := &Strm115AuthStartResult{SessionID: sessionID}
	switch authSource {
	case string(cloud115.SourceTypeBuiltInAppID), string(cloud115.SourceTypeCustomAppID):
		// 官方设备码扫码
		client := cloud115.NewOpenClient(source.AppID, "", "")
		qr, err := client.GetQrCode()
		if err != nil {
			return nil, fmt.Errorf("获取 115 授权二维码失败：%w", err)
		}
		session.Mode = "qrcode"
		session.QrCode = qr
		session.Provider, _ = cloud115.GetOAuthProvider(source)
		result.Mode = "qrcode"
		result.ExpiresIn = 300
		result.QRCode = &struct {
			UID    string `json:"uid"`
			Time   int64  `json:"time"`
			Sign   string `json:"sign"`
			Qrcode string `json:"qrcode"`
		}{UID: qr.Uid, Time: qr.Time, Sign: qr.Sign, Qrcode: qr.Qrcode}
	default:
		// 中继 / MoviePilot / CloudDrive 网页授权
		oauthProvider, err := cloud115.GetOAuthProvider(source)
		if err != nil {
			return nil, err
		}
		oauthResult, err := oauthProvider.BuildAuth(ctx, cloud115.OAuthURLRequest{
			Source:          source,
			RedirectURL:     redirectURL,
			AuthorizationID: sessionID,
		})
		if err != nil {
			return nil, fmt.Errorf("发起 115 授权失败：%w", err)
		}
		session.Provider = oauthProvider
		session.State = oauthResult.State
		if oauthResult.Polling {
			session.Mode = "poll"
		} else {
			session.Mode = "callback"
		}
		result.Mode = "url"
		result.AuthURL = oauthResult.AuthURL
		result.State = oauthResult.State
		result.ExpiresIn = oauthResult.ExpiresIn
		if result.ExpiresIn == 0 {
			result.ExpiresIn = 300
		}
	}

	s.mu.Lock()
	if s.oauthSessions == nil {
		s.oauthSessions = map[string]*strm115AuthSession{}
	}
	s.oauthSessions[sessionID] = session
	s.mu.Unlock()
	s.sweep115AuthSessions()
	return result, nil
}

// Poll115OAuth 轮询授权状态；确认后把 token 写入账号。
func (s *StrmService) Poll115OAuth(ctx context.Context, sessionID string) (*Strm115AuthStatus, error) {
	s.mu.Lock()
	session := s.oauthSessions[sessionID]
	s.mu.Unlock()
	if session == nil {
		return nil, errors.New("授权会话不存在或已过期，请重新发起")
	}
	if time.Since(session.CreatedAt) > strm115AuthSessionTTL {
		s.drop115AuthSession(sessionID)
		return &Strm115AuthStatus{Status: "expired", Tip: "授权会话已过期"}, nil
	}

	var status cloud115.QrCodeScanStatus
	var token *cloud115.OAuthTokenResult
	switch session.Mode {
	case "qrcode":
		client := cloud115.NewOpenClient(session.Source.AppID, "", "")
		st, err := client.QrCodeScanStatus(&session.QrCode.QrCodeData)
		if err != nil {
			return nil, fmt.Errorf("查询扫码状态失败：%w", err)
		}
		status = st
		if st == cloud115.QrCodeScanStatusConfirmed {
			t, err := client.GetToken(session.QrCode)
			if err != nil {
				return nil, fmt.Errorf("获取访问令牌失败：%w", err)
			}
			token = &cloud115.OAuthTokenResult{
				AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresIn: t.ExpiresIn, Done: true,
			}
		}
	case "poll":
		if session.Provider == nil {
			return nil, errors.New("授权服务不可用")
		}
		t, err := session.Provider.Poll(ctx, session.State)
		if err != nil {
			return nil, err
		}
		if t.Done {
			token = &t
		} else {
			return &Strm115AuthStatus{Status: "waiting", Tip: "等待授权确认"}, nil
		}
	case "callback":
		select {
		case t := <-session.TokenCh:
			if t == nil || !t.Done {
				return &Strm115AuthStatus{Status: "waiting", Tip: "等待授权确认"}, nil
			}
			token = t
		default:
			return &Strm115AuthStatus{Status: "waiting", Tip: "正在等待网页授权完成（5 分钟内）"}, nil
		}
	default:
		return nil, fmt.Errorf("未知的授权模式：%s", session.Mode)
	}

	switch status {
	case cloud115.QrCodeScanStatusNotScanned:
		return &Strm115AuthStatus{Status: "waiting", Tip: "等待扫码"}, nil
	case cloud115.QrCodeScanStatusScanned:
		return &Strm115AuthStatus{Status: "scanned", Tip: "已扫码，请在 115 客户端确认"}, nil
	case cloud115.QrCodeScanStatusExpired:
		s.drop115AuthSession(sessionID)
		return &Strm115AuthStatus{Status: "expired", Tip: "二维码已过期"}, nil
	}
	if token == nil || !token.Done {
		return &Strm115AuthStatus{Status: "waiting", Tip: "授权处理中"}, nil
	}
	if err := s.save115OAuthToken(ctx, session, token); err != nil {
		return nil, err
	}
	s.drop115AuthSession(sessionID)
	return &Strm115AuthStatus{Status: "confirmed", Tip: "授权成功"}, nil
}

// Handle115OAuthCallback 处理中继/CloudDrive 授权回跳（公开端点）。
func (s *StrmService) Handle115OAuthCallback(ctx context.Context, payload map[string]string) error {
	authID := strings.TrimSpace(payload["authorization_id"])
	if authID == "" {
		return errors.New("缺少 authorization_id")
	}
	s.mu.Lock()
	session := s.oauthSessions[authID]
	s.mu.Unlock()
	if session == nil {
		return errors.New("授权会话不存在或已过期")
	}
	if session.Provider == nil {
		return errors.New("该授权会话不支持回调")
	}
	token, err := session.Provider.Confirm(ctx, payload)
	if err != nil {
		return err
	}
	if !token.Done {
		return errors.New("回调未包含有效访问凭证")
	}
	select {
	case session.TokenCh <- &token:
	default:
	}
	return nil
}

// save115OAuthToken 把授权 token 写入账号配置并附带用户信息。
func (s *StrmService) save115OAuthToken(ctx context.Context, session *strm115AuthSession, token *cloud115.OAuthTokenResult) error {
	acct, err := s.repo.StrmAccount.FindByID(ctx, session.AccountID)
	if err != nil || acct == nil {
		return errNotFoundOr(err, "网盘账号不存在")
	}
	cfg, err := s.strmAccountConfig(acct)
	if err != nil {
		return err
	}
	cfg["app_id"] = session.Source.AppID
	cfg["access_token"] = s.crypto.Encrypt(token.AccessToken)
	cfg["refresh_token"] = s.crypto.Encrypt(token.RefreshToken)
	// 尝试补充用户信息（失败不阻塞授权）
	if client := cloud115.NewOpenClient(session.Source.AppID, token.AccessToken, token.RefreshToken); client != nil {
		if info, err := client.FetchUserInfo(ctx); err == nil && info != nil {
			cfg["user_id"] = info.UserId.String()
			cfg["user_name"] = info.UserName
			if strings.TrimSpace(acct.Name) == "" || strings.HasPrefix(acct.Name, "115") || acct.Name == providerLabel(model.StrmProvider115) {
				acct.Name = firstNonEmpty(info.UserName, acct.Name)
			}
		}
	}
	enc, err := s.strmAccountConfigJSON(cfg, false)
	if err != nil {
		return err
	}
	acct.Config = enc
	now := time.Now()
	acct.LastTestAt = &now
	acct.LastTestResult = "授权成功"
	acct.LastTestOK = true
	if err := s.repo.StrmAccount.Update(ctx, acct); err != nil {
		return err
	}
	s.invalidate115Provider(acct.ID)
	return nil
}

func (s *StrmService) drop115AuthSession(sessionID string) {
	s.mu.Lock()
	delete(s.oauthSessions, sessionID)
	s.mu.Unlock()
}

func (s *StrmService) sweep115AuthSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.oauthSessions {
		if time.Since(session.CreatedAt) > strm115AuthSessionTTL {
			delete(s.oauthSessions, id)
		}
	}
}

// resolve115Source 解析授权来源。
func resolve115Source(authSource, provider, appID string) (cloud115.Source, error) {
	authSource = strings.TrimSpace(authSource)
	appID = strings.TrimSpace(appID)
	switch authSource {
	case string(cloud115.SourceTypeBuiltInAppID):
		if source, ok := cloud115.FindSource(cloud115.SourceTypeBuiltInAppID, cloud115.ProviderOfficialPKCE, appID); ok {
			return source, nil
		}
		return cloud115.Source{}, fmt.Errorf("未知的内置应用 ID：%s", appID)
	case string(cloud115.SourceTypeCustomAppID):
		if appID == "" {
			return cloud115.Source{}, errors.New("自定义 APP ID 不能为空")
		}
		return cloud115.Source{SourceType: cloud115.SourceTypeCustomAppID, Provider: cloud115.ProviderOfficialPKCE, AppID: appID, AppName: cloud115.CustomAppName, DisplayName: cloud115.CustomAppName}, nil
	case string(cloud115.SourceTypeBuiltInRelay):
		if source, ok := cloud115.FindSource(cloud115.SourceTypeBuiltInRelay, cloud115.AuthProvider(provider), appID); ok {
			return source, nil
		}
		return cloud115.Source{}, errors.New("未知的中继授权服务")
	case string(cloud115.SourceTypeThirdPartyService):
		if source, ok := cloud115.FindSource(cloud115.SourceTypeThirdPartyService, cloud115.AuthProvider(provider), appID); ok {
			return source, nil
		}
		return cloud115.Source{}, errors.New("未知的第三方授权服务")
	default:
		return cloud115.Source{}, errors.New("不支持的授权来源")
	}
}

// refresh115TokensLoop 定期刷新 115 开放平台访问令牌。
func (s *StrmService) refresh115TokensLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.refresh115TokensOnce(ctx)
		}
	}
}

func (s *StrmService) refresh115TokensOnce(ctx context.Context) {
	accounts, err := s.repo.StrmAccount.List(ctx)
	if err != nil {
		return
	}
	for i := range accounts {
		acct := &accounts[i]
		if acct.Provider != model.StrmProvider115 || !acct.Enabled {
			continue
		}
		cfg, err := s.strmAccountConfig(acct)
		if err != nil || cfg["access_token"] == "" || cfg["refresh_token"] == "" {
			continue
		}
		// 临期才刷：115 refresh_token 是一次性轮转，无条件周期刷新会与
		// 运行中任务的自动刷新互相作废对方的凭据。24h 内刷新过（含运行
		// 中回调落库）就跳过；access_token 有效期远长于 24h。
		if last := strings.TrimSpace(cfg["token_refreshed_at"]); last != "" {
			if ts, perr := strconv.ParseInt(last, 10, 64); perr == nil {
				if time.Since(time.Unix(ts, 0)) < 24*time.Hour {
					continue
				}
			}
		}
		provider, err := s.providerFor(ctx, acct)
		if err != nil {
			continue
		}
		openProvider, ok := provider.(interface{ OpenClient() *cloud115.OpenClient })
		if !ok || openProvider.OpenClient() == nil {
			continue
		}
		token, err := openProvider.OpenClient().RefreshToken("")
		if err != nil {
			msg := "令牌刷新失败：" + err.Error()
			if cloud115.IsRefreshTokenDead(err) {
				msg = "授权已失效，请重新授权：" + err.Error()
				cfg["access_token"] = ""
			}
			now := time.Now()
			acct.LastTestAt = &now
			acct.LastTestResult = msg
			acct.LastTestOK = false
			if cfg["access_token"] == "" {
				enc, encErr := s.strmAccountConfigJSON(cfg, false)
				if encErr == nil {
					acct.Config = enc
				}
			}
			_ = s.repo.StrmAccount.Update(ctx, acct)
			s.log.Warn("115 token refresh failed", zap.String("account", acct.Name), zap.String("error", msg))
			continue
		}
		cfg["access_token"] = s.crypto.Encrypt(token.AccessToken)
		cfg["refresh_token"] = s.crypto.Encrypt(token.RefreshToken)
		cfg["token_refreshed_at"] = strconv.FormatInt(time.Now().Unix(), 10)
		enc, err := s.strmAccountConfigJSON(cfg, false)
		if err != nil {
			continue
		}
		acct.Config = enc
		now := time.Now()
		acct.LastTestAt = &now
		acct.LastTestResult = "ok"
		acct.LastTestOK = true
		if err := s.repo.StrmAccount.Update(ctx, acct); err != nil {
			s.log.Warn("update 115 token failed", zap.Error(err))
		}
	}
}

// persist115Tokens 把运行中任务自动刷新得到的新令牌加密写回账号配置，
// 并记录刷新时间供定时刷新线程做临期判断。
func (s *StrmService) persist115Tokens(accountID, accessToken, refreshToken string) {
	ctx := context.Background()
	acct, err := s.repo.StrmAccount.FindByID(ctx, accountID)
	if err != nil || acct == nil {
		return
	}
	cfg, err := s.strmAccountConfig(acct)
	if err != nil {
		return
	}
	cfg["access_token"] = s.crypto.Encrypt(accessToken)
	cfg["refresh_token"] = s.crypto.Encrypt(refreshToken)
	cfg["token_refreshed_at"] = strconv.FormatInt(time.Now().Unix(), 10)
	enc, err := s.strmAccountConfigJSON(cfg, false)
	if err != nil {
		return
	}
	acct.Config = enc
	now := time.Now()
	acct.LastTestAt = &now
	acct.LastTestResult = "ok"
	acct.LastTestOK = true
	if err := s.repo.StrmAccount.Update(ctx, acct); err != nil {
		s.log.Warn("persist refreshed 115 token failed", zap.Error(err), zap.String("account", acct.Name))
		return
	}
	s.log.Info("115 token refreshed and persisted", zap.String("account", acct.Name))
}

// sync115RelayKey 把设置里的中继密钥同步给 cloud115（启动与设置保存时调用）。
func (s *StrmService) sync115RelayKey(ctx context.Context) {
	cloud115.RelayEncryptionKey = s.strmSetting(ctx, Strm115RelayKeySetting)
}

// newStrmID 生成授权会话 ID。
func newStrmID() string {
	return "auth-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "") + "-" + cloud115.RandomString(8)
}
