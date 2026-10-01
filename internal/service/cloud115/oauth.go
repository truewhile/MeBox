// 115 OAuth 授权来源：内置应用（设备码扫码）、QMediaSync/MQFamily 中继
// （需共享 AES 密钥）、MoviePilot 轮询、CloudDrive 回跳。
// 逻辑移植自 QMediaSync 的 v115auth。
package cloud115

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OAuthURLRequest 是发起授权的上下文。
type OAuthURLRequest struct {
	Source          Source
	RedirectURL     string // 本服务回调地址（中继/CloudDrive 回跳用）
	AuthorizationID string
	QRCode          *QrCodeDataReturn `json:"-"` // 设备码模式：调用方预取的二维码
}

// OAuthURLResult 是授权发起结果。
type OAuthURLResult struct {
	AuthURL   string `json:"auth_url,omitempty"`
	State     string `json:"state,omitempty"`
	Polling   bool   `json:"polling"`
	ExpiresIn int64  `json:"expires_in,omitempty"`
	// QRCode 非空表示设备码扫码模式（uid/time/sign/qrcode 一并返回）
	QRCode *QrCodeDataReturn `json:"qrcode,omitempty"`
}

// OAuthTokenResult 是授权完成结果。
type OAuthTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Done         bool
}

// OAuthProvider 描述一种 115 授权来源。
type OAuthProvider interface {
	BuildAuth(ctx context.Context, req OAuthURLRequest) (OAuthURLResult, error)
	Confirm(ctx context.Context, payload map[string]string) (OAuthTokenResult, error)
	Poll(ctx context.Context, state string) (OAuthTokenResult, error)
}

var errUnsupportedOAuthOperation = errors.New("当前授权服务不支持此操作")

// GetOAuthProvider 按授权来源取 provider：
//   - official_pkce：设备码扫码（需 AppID）
//   - qmediasync/mqfamily：中继授权（需配置 strm.115_relay_key）
//   - moviepilot / clouddrive：第三方服务
func GetOAuthProvider(source Source) (OAuthProvider, error) {
	switch source.Provider {
	case ProviderOfficialPKCE:
		if strings.TrimSpace(source.AppID) == "" {
			return nil, fmt.Errorf("缺少 115 开放平台应用 ID")
		}
		return deviceCodeOAuthProvider{}, nil
	case ProviderQMediaSync, ProviderMQFamily:
		if !RelayAvailable() {
			return nil, fmt.Errorf("中继授权需要配置共享加密密钥（设置 strm.115_relay_key）")
		}
		return relayOAuthProvider{source: source}, nil
	case ProviderMoviePilot:
		return moviePilotOAuthProvider{authServer: oauthServerOr(source.AuthServer, "https://movie-pilot.org")}, nil
	case ProviderCloudDrive:
		return cloudDriveOAuthProvider{source: source}, nil
	default:
		return nil, fmt.Errorf("不支持的 115 授权来源")
	}
}

func oauthServerOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

// deviceCodeOAuthProvider 走 115 官方设备码扫码（无回调、无共享密钥）。
type deviceCodeOAuthProvider struct{}

func (deviceCodeOAuthProvider) BuildAuth(_ context.Context, req OAuthURLRequest) (OAuthURLResult, error) {
	if req.QRCode != nil {
		return OAuthURLResult{QRCode: req.QRCode, ExpiresIn: 300}, nil
	}
	return OAuthURLResult{}, fmt.Errorf("缺少设备码数据")
}

func (deviceCodeOAuthProvider) Confirm(_ context.Context, _ map[string]string) (OAuthTokenResult, error) {
	return OAuthTokenResult{}, errUnsupportedOAuthOperation
}

func (deviceCodeOAuthProvider) Poll(_ context.Context, _ string) (OAuthTokenResult, error) {
	return OAuthTokenResult{}, errUnsupportedOAuthOperation
}

// relayOAuthProvider 走 QMediaSync/MQFamily 中继授权。
type relayOAuthProvider struct {
	source Source
}

func (provider relayOAuthProvider) BuildAuth(_ context.Context, req OAuthURLRequest) (OAuthURLResult, error) {
	clientID := strings.TrimSpace(provider.source.AppID)
	if clientID == "" {
		clientID = BuiltInRelayQ115STRM
		if provider.source.Provider == ProviderQMediaSync {
			clientID = BuiltInRelayQMediaSync
		}
	}
	redirectURL := strings.TrimSpace(req.RedirectURL)
	if redirectURL != "" {
		var err error
		redirectURL, err = appendCallbackParams(redirectURL, url.Values{
			"source":           []string{"115"},
			"authorization_id": []string{req.AuthorizationID},
		})
		if err != nil {
			return OAuthURLResult{}, err
		}
	}
	stateObj := struct {
		State           string `json:"state"`
		Time            int64  `json:"time"`
		ClientId        string `json:"client_id"`
		RedirectURL     string `json:"redirect_url"`
		AuthorizationID string `json:"authorization_id,omitempty"`
	}{
		State:           RandomString(16),
		Time:            time.Now().Unix(),
		ClientId:        clientID,
		RedirectURL:     redirectURL,
		AuthorizationID: req.AuthorizationID,
	}
	stateJSON, _ := json.Marshal(stateObj)
	stateEncoded, err := EncryptRelay(string(stateJSON))
	if err != nil {
		return OAuthURLResult{}, err
	}
	baseURL := oauthServerOr(provider.source.AuthServer, "https://api.mqfamily.top")
	if provider.source.Provider == ProviderQMediaSync {
		baseURL = oauthServerOr(provider.source.AuthServer, "https://oauth.qmediasync.cn")
	}
	return OAuthURLResult{AuthURL: fmt.Sprintf("%s/115.php?action=code&state=%s", baseURL, stateEncoded)}, nil
}

func (provider relayOAuthProvider) Confirm(_ context.Context, payload map[string]string) (OAuthTokenResult, error) {
	data := payload["data"]
	if data == "" {
		return OAuthTokenResult{}, fmt.Errorf("缺少中转回调数据")
	}
	decryptedData, err := DecryptRelay(data)
	if err != nil {
		return OAuthTokenResult{}, err
	}
	var resp struct {
		Data struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		} `json:"data"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(decryptedData), &resp); err != nil {
		return OAuthTokenResult{}, err
	}
	if resp.Data.AccessToken == "" || resp.Data.RefreshToken == "" {
		if resp.Error != "" {
			return OAuthTokenResult{}, errors.New(resp.Error)
		}
		if resp.Message != "" {
			return OAuthTokenResult{}, errors.New(resp.Message)
		}
		return OAuthTokenResult{}, fmt.Errorf("中转回调未返回访问凭证")
	}
	return OAuthTokenResult{AccessToken: resp.Data.AccessToken, RefreshToken: resp.Data.RefreshToken, ExpiresIn: resp.Data.ExpiresIn, Done: true}, nil
}

func (provider relayOAuthProvider) Poll(_ context.Context, _ string) (OAuthTokenResult, error) {
	return OAuthTokenResult{}, errUnsupportedOAuthOperation
}

// moviePilotOAuthProvider 走 MoviePilot 的 115 授权服务（轮询）。
type moviePilotOAuthProvider struct {
	authServer string
}

func (provider moviePilotOAuthProvider) BuildAuth(ctx context.Context, req OAuthURLRequest) (OAuthURLResult, error) {
	endpoint := provider.authServer + "/u115/auth_url"
	resp, err := httpGetJSON(ctx, endpoint)
	if err != nil {
		return OAuthURLResult{}, err
	}
	authURL := stringField(resp, "auth_url")
	state := stringField(resp, "state")
	if authURL == "" || state == "" {
		return OAuthURLResult{}, fmt.Errorf("MoviePilot 授权服务响应缺少 auth_url 或 state")
	}
	return OAuthURLResult{AuthURL: authURL, State: state, Polling: true, ExpiresIn: 300}, nil
}

func (provider moviePilotOAuthProvider) Confirm(_ context.Context, _ map[string]string) (OAuthTokenResult, error) {
	return OAuthTokenResult{}, errUnsupportedOAuthOperation
}

func (provider moviePilotOAuthProvider) Poll(ctx context.Context, state string) (OAuthTokenResult, error) {
	if state == "" {
		return OAuthTokenResult{}, fmt.Errorf("缺少授权状态")
	}
	endpoint := provider.authServer + "/u115/token?state=" + url.QueryEscape(state)
	resp, err := httpGetJSON(ctx, endpoint)
	if err != nil {
		return OAuthTokenResult{}, err
	}
	return tokenResultFromMap(resp), nil
}

// cloudDriveOAuthProvider 走 CloudDrive 中转（115 授权页 → zhenyunpan 换 token → 回跳）。
type cloudDriveOAuthProvider struct {
	source Source
}

func (provider cloudDriveOAuthProvider) BuildAuth(_ context.Context, req OAuthURLRequest) (OAuthURLResult, error) {
	if strings.TrimSpace(req.RedirectURL) == "" {
		return OAuthURLResult{}, fmt.Errorf("CloudDrive 授权需要回跳地址")
	}
	callback, err := appendCallbackParams(req.RedirectURL, url.Values{
		"source":           []string{"115"},
		"authorization_id": []string{req.AuthorizationID},
	})
	if err != nil {
		return OAuthURLResult{}, err
	}
	clientID := strings.TrimSpace(provider.source.AppID)
	if clientID == "" {
		clientID = "100195313"
	}
	redirectURI := strings.TrimSpace(provider.source.RedirectURI)
	if redirectURI == "" {
		redirectURI = "https://redirect115.zhenyunpan.com"
	}
	authURL, _ := url.Parse("https://passportapi.115.com/open/authorize")
	query := authURL.Query()
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("state", callback)
	authURL.RawQuery = query.Encode()
	return OAuthURLResult{AuthURL: authURL.String(), ExpiresIn: 300}, nil
}

func (provider cloudDriveOAuthProvider) Confirm(_ context.Context, payload map[string]string) (OAuthTokenResult, error) {
	expiresIn, _ := strconv.ParseInt(payload["expires_in"], 10, 64)
	token := OAuthTokenResult{
		AccessToken:  payload["access_token"],
		RefreshToken: payload["refresh_token"],
		ExpiresIn:    expiresIn,
	}
	token.Done = token.AccessToken != "" && token.RefreshToken != ""
	if !token.Done {
		return OAuthTokenResult{}, fmt.Errorf("CloudDrive 回调未返回访问凭证")
	}
	return token, nil
}

func (provider cloudDriveOAuthProvider) Poll(_ context.Context, _ string) (OAuthTokenResult, error) {
	return OAuthTokenResult{}, errUnsupportedOAuthOperation
}

// ─── 工具 ──────────────────────────────────────────────────────────────────────

func appendCallbackParams(rawURL string, params url.Values) (string, error) {
	callbackURL, err := url.Parse(rawURL)
	if err != nil || callbackURL.Scheme == "" || callbackURL.Host == "" {
		return "", fmt.Errorf("OAuth 回跳地址无效")
	}
	if callbackURL.Fragment != "" {
		fragmentPath, fragmentQuery, hasQuery := strings.Cut(callbackURL.Fragment, "?")
		fragmentValues := url.Values{}
		if hasQuery {
			fragmentValues, err = url.ParseQuery(fragmentQuery)
			if err != nil {
				return "", err
			}
		}
		for key, values := range params {
			for _, value := range values {
				if value != "" {
					fragmentValues.Set(key, value)
				}
			}
		}
		callbackURL.Fragment = fragmentPath + "?" + fragmentValues.Encode()
		return callbackURL.String(), nil
	}
	query := callbackURL.Query()
	for key, values := range params {
		for _, value := range values {
			if value != "" {
				query.Set(key, value)
			}
		}
	}
	callbackURL.RawQuery = query.Encode()
	return callbackURL.String(), nil
}

// oauthHTTPClient 是 OAuth 授权服务专用 HTTP 客户端。http.DefaultClient 无超时，
// 授权服务无响应时会永久阻塞授权/轮询协程，这里统一 30s 超时（ctx 仍经
// NewRequestWithContext 传导，可提前取消）。
var oauthHTTPClient = &http.Client{Timeout: 30 * time.Second}

func httpGetJSON(ctx context.Context, endpoint string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", DefaultUA)
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("授权服务返回 HTTP %d：%s", resp.StatusCode, string(body))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if nested, ok := data["data"].(map[string]any); ok {
		for key, value := range nested {
			if _, exists := data[key]; !exists {
				data[key] = value
			}
		}
	}
	return data, nil
}

func tokenResultFromMap(data map[string]any) OAuthTokenResult {
	if nested, ok := data["data"].(map[string]any); ok {
		for key, value := range nested {
			if _, exists := data[key]; !exists {
				data[key] = value
			}
		}
	}
	expiresIn := int64(0)
	switch value := data["expires_in"].(type) {
	case float64:
		expiresIn = int64(value)
	case json.Number:
		expiresIn, _ = value.Int64()
	case string:
		expiresIn, _ = strconv.ParseInt(value, 10, 64)
	}
	token := OAuthTokenResult{
		AccessToken:  stringField(data, "access_token"),
		RefreshToken: stringField(data, "refresh_token"),
		ExpiresIn:    expiresIn,
	}
	token.Done = token.AccessToken != "" && token.RefreshToken != ""
	return token
}

func stringField(data map[string]any, key string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// ─── 中继回调 AES 加解密（与 QMediaSync 同算法：AES-256-CBC + URL-safe Base64） ───

// EncryptRelay 加密中继授权 state / 回调数据。
func EncryptRelay(plaintext string) (string, error) {
	return encryptAES(plaintext, RelayEncryptionKey)
}

// DecryptRelay 解密中继回调数据。
func DecryptRelay(encrypted string) (string, error) {
	return decryptAES(encrypted, RelayEncryptionKey)
}

func encryptAES(plaintext, keyText string) (string, error) {
	if keyText == "" {
		return "", errors.New("加密密钥不能为空（请配置 strm.115_relay_key）")
	}
	keyHash := sha256.Sum256([]byte(keyText))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return "", err
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	padtext := append([]byte(plaintext), make([]byte, padding)...)
	for i := 0; i < padding; i++ {
		padtext[len(plaintext)+i] = byte(padding)
	}
	ciphertext := make([]byte, aes.BlockSize+len(padtext))
	iv := ciphertext[:aes.BlockSize]
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext[aes.BlockSize:], padtext)
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	encoded = strings.ReplaceAll(encoded, "+", "-")
	encoded = strings.ReplaceAll(encoded, "/", "_")
	return strings.TrimRight(encoded, "="), nil
}

func decryptAES(encrypted, keyText string) (string, error) {
	if keyText == "" {
		return "", errors.New("加密密钥不能为空（请配置 strm.115_relay_key）")
	}
	encrypted = strings.ReplaceAll(encrypted, "-", "+")
	encrypted = strings.ReplaceAll(encrypted, "_", "/")
	switch len(encrypted) % 4 {
	case 2:
		encrypted += "=="
	case 3:
		encrypted += "="
	}
	data, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}
	if len(data) < aes.BlockSize {
		return "", errors.New("密文长度不足")
	}
	keyHash := sha256.Sum256([]byte(keyText))
	block, err := aes.NewCipher(keyHash[:])
	if err != nil {
		return "", err
	}
	iv := data[:aes.BlockSize]
	ciphertext := data[aes.BlockSize:]
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(ciphertext, ciphertext)
	if len(ciphertext) == 0 {
		return "", errors.New("解密结果为空")
	}
	padding := int(ciphertext[len(ciphertext)-1])
	if padding <= 0 || padding > aes.BlockSize {
		return "", errors.New("解密填充非法")
	}
	return string(ciphertext[:len(ciphertext)-padding]), nil
}
