// EmbyRemoteService 是「远程 Emby 联邦聚合」核心：把挂载的远程 Emby 服务器
// 作为外部媒体源，通过 MeBox 的 Emby 兼容 API 透出。
//
// 设计要点：
//   - 远程媒体的元数据完全不落库：每次请求实时向远程 Emby 拉取；
//   - 条目 ID 用 embyremote~{accountID}~{remoteID} 伪装（见 emby_remote_ids.go），
//     客户端拿伪装 ID 回来时按账号路由回远程；
//   - 播放分流由账号级 proxy_play 配置决定：
//     不代理（默认）= MediaSource 下发热门远程绝对 URL，播放字节完全不经过 MeBox；
//     代理 = 下发 MeBox 本地 /Videos/{encoded} 端点，由 ProxyVideoStream 反向拉流。
//
// 配置复用 STRM 账号体系（StrmAccount.Provider = emby_remote），CRUD/加密/连通
// 测试全部走既有 /admin/strm/accounts 接口，不需要新增数据表。
package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// embyRemoteHTTPTimeout 远程 Emby 常规 API 请求超时（流式代理不在此列）。
const embyRemoteHTTPTimeout = 15 * time.Second

// embyRemoteUA 桌面浏览器 UA：远程 Emby 前方若有 Cloudflare/WAF 会拦截
// Go-http-client 等非浏览器 UA（403 error code: 1010），必须伪装浏览器。
const embyRemoteUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// embyRemoteTransport 统一给远程请求注入浏览器 UA。
type embyRemoteTransport struct {
	base http.RoundTripper
}

func (t *embyRemoteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.TrimSpace(req.Header.Get("User-Agent")) == "" {
		req.Header.Set("User-Agent", embyRemoteUA)
	}
	return t.base.RoundTrip(req)
}

// EmbyRemoteConfig 是一个远程 Emby 账号的解密配置。
type EmbyRemoteConfig struct {
	BaseURL      string           // 当前生效线路地址（http://host:8096，无需 /emby 后缀）
	Lines        []EmbyRemoteLine // 全部线路，按优先级排列
	ActiveLine   int              // 当前生效线路下标
	Username     string
	Password     string
	Token        string // api_key（手动填写或自动认证获得）
	RemoteUserID string // 远程用户 Id（自动认证后回填）
	ProxyPlay    bool   // true=播放流量经 MeBox 反向代理；false=客户端直连远程
}

// EmbyRemoteService 提供对远程 Emby 服务器的读写封装。
type EmbyRemoteService struct {
	cfg    *config.Config
	log    *zap.Logger
	repo   *repository.Container
	crypto *CryptoService
	http   *http.Client
	stream *http.Client // 流式代理专用（视频/字幕），无整体 Timeout
	cache  *RuntimeCacheService
}

// NewEmbyRemoteService 构造远程 Emby 聚合服务。
func NewEmbyRemoteService(cfg *config.Config, log *zap.Logger, repo *repository.Container, crypto *CryptoService) *EmbyRemoteService {
	return &EmbyRemoteService{
		cfg:    cfg,
		log:    log,
		repo:   repo,
		crypto: crypto,
		http: &http.Client{
			Timeout:   embyRemoteHTTPTimeout,
			Transport: &embyRemoteTransport{base: http.DefaultTransport},
		},
		// 流式代理必须用无整体 Timeout 的 client：http.Client.Timeout
		// 覆盖整个响应体读取过程，15s 的常规超时会让代理播放播到
		// 15 秒整被掐断。生命周期由请求 ctx 控制。
		stream: &http.Client{
			Transport: &embyRemoteTransport{base: http.DefaultTransport},
		},
	}
}

func (r *EmbyRemoteService) SetRuntimeCache(cache *RuntimeCacheService) *EmbyRemoteService {
	if r != nil {
		r.cache = cache
	}
	return r
}

func (r *EmbyRemoteService) remoteMediaCacheTTL() time.Duration {
	seconds := 15
	if r != nil && r.cfg != nil && r.cfg.Cache.MediaTTLSeconds > 0 {
		seconds = r.cfg.Cache.MediaTTLSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (r *EmbyRemoteService) remoteCacheKey(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "|")))
	return "media:embyremote:" + hex.EncodeToString(sum[:])
}

func (r *EmbyRemoteService) invalidateRemoteMediaCache(ctx context.Context) {
	if r != nil && r.cache != nil {
		r.cache.DeletePrefix(ctx, "media:embyremote:")
	}
}

// ListAccounts 返回全部启用的远程 Emby 挂载账号。
func (r *EmbyRemoteService) ListAccounts(ctx context.Context) ([]model.StrmAccount, error) {
	accounts, err := r.repo.StrmAccount.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.StrmAccount, 0, len(accounts))
	for i := range accounts {
		if accounts[i].Provider == model.StrmProviderEmbyRemote {
			out = append(out, accounts[i])
		}
	}
	return out, nil
}

// ConfiguredRemoteHosts 返回所有已配置的远程 Emby 线路的主机名/IP（去重、不含端口）。
func (r *EmbyRemoteService) ConfiguredRemoteHosts(ctx context.Context) []string {
	if r == nil || r.repo == nil || r.repo.StrmAccount == nil {
		return nil
	}
	accounts, err := r.ListAccounts(ctx)
	if err != nil || len(accounts) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var hosts []string
	for _, acct := range accounts {
		lines, _, err := r.LinesOf(&acct)
		if err != nil {
			continue
		}
		for _, line := range lines {
			u, err := url.Parse(line.URL)
			if err != nil || u.Hostname() == "" {
				continue
			}
			h := strings.ToLower(u.Hostname())
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// AccountByID 按 ID 查找远程 Emby 挂载账号（不存在或类型不符返回 nil）。
func (r *EmbyRemoteService) AccountByID(ctx context.Context, id string) *model.StrmAccount {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	acct, err := r.repo.StrmAccount.FindByID(ctx, id)
	if err != nil || acct == nil {
		return nil
	}
	if acct.Provider != model.StrmProviderEmbyRemote || !acct.Enabled {
		return nil
	}
	return acct
}

// ─── 媒体库挂载管理 ─────────────────────────────────────────────────────────────

// ListMounts 返回全部挂载。
func (r *EmbyRemoteService) ListMounts(ctx context.Context) ([]model.EmbyMount, error) {
	return r.repo.EmbyMount.List(ctx)
}

// ListMountsByAccount 返回指定账号的挂载。
func (r *EmbyRemoteService) ListMountsByAccount(ctx context.Context, accountID string) ([]model.EmbyMount, error) {
	return r.repo.EmbyMount.ListByAccountID(ctx, accountID)
}

// MountByID 按 ID 查挂载。
func (r *EmbyRemoteService) MountByID(ctx context.Context, id string) (*model.EmbyMount, error) {
	return r.repo.EmbyMount.FindByID(ctx, id)
}

// CreateMount 创建一个挂载（校验账号类型与远程 View 编号）。
func (r *EmbyRemoteService) CreateMount(ctx context.Context, m *model.EmbyMount) (*model.EmbyMount, error) {
	if strings.TrimSpace(m.AccountID) == "" || strings.TrimSpace(m.RemoteViewID) == "" {
		return nil, errors.New("缺少账号或远程媒体库")
	}
	if r.AccountByID(ctx, m.AccountID) == nil {
		return nil, errors.New("远程 Emby 账号不存在或已禁用")
	}
	if err := r.repo.EmbyMount.Create(ctx, m); err != nil {
		return nil, err
	}
	r.invalidateRemoteMediaCache(ctx)
	return m, nil
}

// CreateMounts 批量创建挂载（幂等：已存在的远程库自动跳过）。
func (r *EmbyRemoteService) CreateMounts(ctx context.Context, mounts []*model.EmbyMount) (int, error) {
	if len(mounts) == 0 {
		return 0, nil
	}
	existing, err := r.repo.EmbyMount.ListByAccountID(ctx, mounts[0].AccountID)
	if err != nil {
		return 0, err
	}
	have := make(map[string]bool, len(existing))
	for _, e := range existing {
		have[e.RemoteViewID] = true
	}
	fresh := make([]*model.EmbyMount, 0, len(mounts))
	for _, m := range mounts {
		if m == nil || have[m.RemoteViewID] {
			continue
		}
		fresh = append(fresh, m)
	}
	if len(fresh) == 0 {
		return 0, nil
	}
	if err := r.repo.EmbyMount.CreateInBatches(ctx, fresh, 50); err != nil {
		return 0, err
	}
	r.invalidateRemoteMediaCache(ctx)
	return len(fresh), nil
}

// UpdateMount 更新挂载（名称 / 代理 / 启用）。
func (r *EmbyRemoteService) UpdateMount(ctx context.Context, id string, m *model.EmbyMount) (*model.EmbyMount, error) {
	existing, err := r.repo.EmbyMount.FindByID(ctx, id)
	if err != nil || existing == nil {
		return nil, errNotFoundOr(err, "挂载不存在")
	}
	existing.Name = strings.TrimSpace(m.Name)
	existing.ProxyPlay = m.ProxyPlay
	existing.Enabled = m.Enabled
	if err := r.repo.EmbyMount.Update(ctx, existing); err != nil {
		return nil, err
	}
	r.invalidateRemoteMediaCache(ctx)
	return existing, nil
}

// DeleteMount 删除挂载。
func (r *EmbyRemoteService) DeleteMount(ctx context.Context, id string) error {
	err := r.repo.EmbyMount.Delete(ctx, id)
	if err == nil {
		r.invalidateRemoteMediaCache(ctx)
	}
	return err
}

// ReorderMounts 批量重排挂载媒体库顺序。
func (r *EmbyRemoteService) ReorderMounts(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := r.repo.EmbyMount.SetSortOrder(ctx, ids); err != nil {
		return err
	}
	r.invalidateRemoteMediaCache(ctx)
	return nil
}

// FullMountAccount 把账号的全部远程媒体库（View）挂载进来（幂等，已存在跳过）。
func (r *EmbyRemoteService) FullMountAccount(ctx context.Context, acct *model.StrmAccount, proxyPlayDefault bool) (int, error) {
	views, err := r.RemoteViews(ctx, acct)
	if err != nil {
		return 0, err
	}
	mounts := make([]*model.EmbyMount, 0, len(views))
	for _, v := range views {
		viewID := remoteItemString(v, "Id")
		if viewID == "" {
			continue
		}
		mounts = append(mounts, &model.EmbyMount{
			AccountID:      acct.ID,
			RemoteViewID:   viewID,
			RemoteViewName: remoteItemString(v, "Name"),
			CollectionType: remoteItemString(v, "CollectionType"),
			ProxyPlay:      proxyPlayDefault,
			Enabled:        true,
		})
	}
	return r.CreateMounts(ctx, mounts)
}

// ResolveMount 按伪装 ID 的第一段（挂载 ID）解析挂载与其所属账号。
// 远程条目/媒体库的伪装 ID 格式：embyremote~{mountID}~{remoteID}。
func (r *EmbyRemoteService) ResolveMount(ctx context.Context, mountID string) (*model.EmbyMount, *model.StrmAccount, error) {
	mount, err := r.repo.EmbyMount.FindByID(ctx, mountID)
	if err != nil || mount == nil || !mount.Enabled {
		return nil, nil, errors.New("挂载不存在或已禁用")
	}
	acct := r.AccountByID(ctx, mount.AccountID)
	if acct == nil {
		return nil, nil, errors.New("远程 Emby 账号不存在或已禁用")
	}
	return mount, acct, nil
}

// AutoSeedMounts 兼容迁移：已有 emby_remote 账号但没有任何挂载时，自动把
// 其全部媒体库挂载进来（代理沿用账号旧配置），保证旧部署升级后媒体库不消失。
// 幂等：每个账号只在挂载数为 0 时执行一次。
func (r *EmbyRemoteService) AutoSeedMounts(ctx context.Context) {
	accounts, err := r.ListAccounts(ctx)
	if err != nil || len(accounts) == 0 {
		return
	}
	for i := range accounts {
		acct := &accounts[i]
		count, err := r.repo.EmbyMount.CountByAccountID(ctx, acct.ID)
		if err != nil || count > 0 {
			continue
		}
		cfg, cfgErr := r.configOf(acct)
		if cfgErr != nil {
			continue
		}
		n, seedErr := r.FullMountAccount(ctx, acct, cfg.ProxyPlay)
		if seedErr != nil {
			if r.log != nil {
				r.log.Warn("auto-seed emby mounts failed",
					zap.String("account", acct.Name), zap.Error(seedErr))
			}
		} else if n > 0 {
			if r.log != nil {
				r.log.Info("auto-seeded emby mounts",
					zap.String("account", acct.Name), zap.Int("mounts", n))
			}
		}
	}
}

// remoteConfigWithToken 解密账号配置并确保已有可用凭据（首次请求自动认证并
// 回写 token 与 remote_user_id，等价于管理端「测试连接」），保证后续构造的
// /Users/{userId} 路径使用远程真实用户 GUID，而不是未认证兜底的 "0"。
func (r *EmbyRemoteService) remoteConfigWithToken(ctx context.Context, acct *model.StrmAccount) (*EmbyRemoteConfig, error) {
	cfg, err := r.configOf(acct)
	if err != nil {
		return nil, err
	}
	if err := r.ensureToken(ctx, acct, cfg); err != nil {
		return nil, err
	}
	// api_key 直连（未配用户名/密码）的账号认证步骤不会回填用户 ID；此时用
	// api_key 拉一次用户列表取真实用户 ID 并回写，避免 /Users/{uid} 请求路径
	// 落回兜底 "0" 被远程 Emby 拒绝（Unrecognized Guid format）。
	if strings.TrimSpace(cfg.RemoteUserID) == "" {
		r.resolveRemoteUserID(ctx, acct, cfg)
	}
	return cfg, nil
}

// resolveRemoteUserID 用已有 api_key 拉远程用户列表，把首个用户 ID 回写账号
// 配置（取不到时静默跳过，保持兜底行为不变）。
func (r *EmbyRemoteService) resolveRemoteUserID(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig) {
	if acct == nil || cfg == nil || strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.RemoteUserID) != "" {
		return
	}
	q := url.Values{"api_key": {cfg.Token}}
	var users []map[string]any
	if err := r.doGet(ctx, acct, cfg, "/Users", q, &users); err != nil || len(users) == 0 {
		return
	}
	uid := strings.TrimSpace(remoteItemString(users[0], "Id"))
	if uid == "" {
		return
	}
	cfg.RemoteUserID = uid
	_ = r.updateAccountConfig(ctx, acct, func(raw map[string]string) {
		raw["remote_user_id"] = uid
	})
}

// CleanupOrphanMounts 清理账号已删除的残留挂载（老版本删除账号未级联），
// 避免挂载计数/列表出现永远清不掉的孤儿数据。
func (r *EmbyRemoteService) CleanupOrphanMounts(ctx context.Context) {
	n, err := r.repo.EmbyMount.DeleteOrphans(ctx)
	if err != nil {
		if r.log != nil {
			r.log.Warn("cleanup orphan emby mounts failed", zap.Error(err))
		}
		return
	}
	if n > 0 {
		r.invalidateRemoteMediaCache(ctx)
		if r.log != nil {
			r.log.Info("cleaned up orphan emby mounts", zap.Int64("mounts", n))
		}
	}
}

// configOf 解密账号配置。
func (r *EmbyRemoteService) configOf(acct *model.StrmAccount) (*EmbyRemoteConfig, error) {
	raw := map[string]string{}
	if acct != nil && strings.TrimSpace(acct.Config) != "" {
		if err := json.Unmarshal([]byte(acct.Config), &raw); err != nil {
			return nil, fmt.Errorf("decode emby account config: %w", err)
		}
	}
	lines, activeLine, err := ParseEmbyRemoteLines(raw)
	if err != nil {
		return nil, err
	}
	cfg := &EmbyRemoteConfig{
		BaseURL:      lines[activeLine].URL,
		Lines:        lines,
		ActiveLine:   activeLine,
		Username:     strings.TrimSpace(raw["username"]),
		Password:     r.crypto.Decrypt(raw["password"]),
		Token:        firstNonEmptyStr(r.crypto.Decrypt(raw["api_key"]), r.crypto.Decrypt(raw["token"])),
		RemoteUserID: strings.TrimSpace(raw["remote_user_id"]),
		ProxyPlay:    parseBoolSetting(raw["proxy_play"], false),
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("缺少 Emby 地址")
	}
	return cfg, nil
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// embyBase 把地址规范为不带尾部斜杠的 /emby 根。
func (r *EmbyRemoteService) embyBase(cfg *EmbyRemoteConfig) string {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/emby") {
		base += "/emby"
	}
	return base
}

// ensureToken 返回可用的 api_key：已有则直接用；否则用用户名/密码认证并回写
// 数据库（自动获得的 token 与 remote_user_id 会加密保存在账号配置里）。
func (r *EmbyRemoteService) ensureToken(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig) error {
	if strings.TrimSpace(cfg.Token) != "" {
		return nil
	}
	if strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.Password) == "" {
		return errors.New("缺少 Emby 凭据：请填写 api_key 或 用户名/密码")
	}
	var lastErr error
	for _, lineIdx := range r.lineOrder(cfg) {
		lineCfg := r.withLine(cfg, lineIdx)
		err := r.ensureTokenOnLine(ctx, acct, lineCfg)
		if err == nil {
			cfg.Token = lineCfg.Token
			cfg.RemoteUserID = lineCfg.RemoteUserID
			cfg.BaseURL = lineCfg.BaseURL
			r.adoptWorkingLine(ctx, acct, cfg, lineIdx)
			return r.persistToken(ctx, acct, cfg)
		}
		lastErr = err
		if !isEmbyLineFailoverError(err) {
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("Emby 线路认证失败")
}

func (r *EmbyRemoteService) ensureTokenOnLine(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig) error {
	body, _ := json.Marshal(map[string]string{"Username": cfg.Username, "Pw": cfg.Password})
	endpoint := r.embyBase(cfg) + "/Users/AuthenticateByName"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="MeBox", Device="MeBox-Federated", DeviceId="mebox-federated", Version="1.0"`)
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接远程 Emby 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("远程 Emby 登录失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var login struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			Id string `json:"Id"`
		} `json:"User"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		return err
	}
	if strings.TrimSpace(login.AccessToken) == "" {
		return errors.New("远程 Emby 未返回 AccessToken")
	}
	cfg.Token = login.AccessToken
	if login.User.Id != "" {
		cfg.RemoteUserID = login.User.Id
	}
	return nil
}

// acctCfgMu 序列化对账号 Config 的读-改-写。并发请求若各自基于请求开始
// 时的快照做整包覆盖，会互相丢失更新（刚持久化的 token / active_line 被
// 旧快照覆盖回去）。
var acctCfgMu sync.Mutex

// updateAccountConfig 在互斥下重读账号最新 Config，应用 mutate 后写回，
// 并同步调用方持有的 acct 快照。
func (r *EmbyRemoteService) updateAccountConfig(ctx context.Context, acct *model.StrmAccount, mutate func(raw map[string]string)) error {
	if acct == nil || r.repo == nil {
		return nil
	}
	acctCfgMu.Lock()
	defer acctCfgMu.Unlock()
	raw := map[string]string{}
	if fresh, err := r.repo.StrmAccount.FindByID(ctx, acct.ID); err == nil && fresh != nil {
		acct.Config = fresh.Config // 以 DB 最新值为基线，避免覆盖并发写入
	}
	if strings.TrimSpace(acct.Config) != "" {
		_ = json.Unmarshal([]byte(acct.Config), &raw)
	}
	if mutate != nil {
		mutate(raw)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	acct.Config = string(data)
	return r.repo.StrmAccount.Update(ctx, acct)
}

// persistToken 把认证得到的 token / user id 加密写回账号配置（下次请求免登录）。
func (r *EmbyRemoteService) persistToken(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig) error {
	if acct == nil {
		return nil
	}
	return r.updateAccountConfig(ctx, acct, func(raw map[string]string) {
		raw["api_key"] = r.crypto.Encrypt(cfg.Token)
		raw["remote_user_id"] = cfg.RemoteUserID
		if strings.TrimSpace(raw["username"]) == "" {
			raw["username"] = cfg.Username
		}
	})
}

// doGet 向远程 Emby 发起带 api_key 的 GET，把响应 JSON 解码到 out。
// 401 时自动重认证一次再重试（凭据过期场景）。连接失败时按线路优先级自动切换。
func (r *EmbyRemoteService) doGet(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig, path string, q url.Values, out any) error {
	var lastErr error
	for _, lineIdx := range r.lineOrder(cfg) {
		lineCfg := r.withLine(cfg, lineIdx)
		err := r.doGetOnLine(ctx, acct, cfg, lineCfg, path, q, out)
		if err == nil {
			r.adoptWorkingLine(ctx, acct, cfg, lineIdx)
			return nil
		}
		lastErr = err
		if !isEmbyLineFailoverError(err) {
			return err
		}
	}
	if lastErr != nil {
		if r.log != nil && acct != nil {
			r.log.Warn("remote emby request failed",
				zap.String("account", acct.Name), zap.String("path", path), zap.Error(lastErr))
		}
		return lastErr
	}
	return errors.New("远程 Emby 请求失败")
}

func (r *EmbyRemoteService) doGetOnLine(ctx context.Context, acct *model.StrmAccount, master *EmbyRemoteConfig, cfg *EmbyRemoteConfig, path string, q url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		if strings.TrimSpace(cfg.Token) == "" {
			if err := r.ensureTokenOnLine(ctx, acct, cfg); err != nil {
				return err
			}
			master.Token = cfg.Token
			master.RemoteUserID = cfg.RemoteUserID
		}
		endpoint := r.embyBase(cfg) + path
		if q != nil {
			endpoint += "?" + q.Encode()
		} else {
			endpoint += "?api_key=" + url.QueryEscape(cfg.Token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-Emby-Token", cfg.Token)
		resp, err := r.http.Do(req)
		if err != nil {
			return fmt.Errorf("请求远程 Emby 失败: %w", err)
		}
		// 读 8MB+1 以区分"刚好 8MB"与"被截断"：截断的 JSON 会让
		// Unmarshal 报 unexpected end，难以定位；这里显式报错。
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if len(data) > 8<<20 {
			return fmt.Errorf("远程 Emby 响应超过 8MB 上限（路径 %s）：请减小分页或 Fields 字段", path)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// 401：只清当前线路的内存 token 并立即重认证；不在此时删除
			// DB 里的 api_key——①外层还会按线路故障转移（其他线路可能
			// 存有自己的 token）；②纯 api_key 账号删除后无法再认证，一次
			// 线路误报就会把账号“砖化”。重认证成功后 persistToken 会用
			// 新 token 覆盖 api_key。
			cfg.Token = ""
			if err := r.ensureTokenOnLine(ctx, acct, cfg); err != nil {
				return fmt.Errorf("认证重试失败: %w", err)
			}
			master.Token = cfg.Token
			master.RemoteUserID = cfg.RemoteUserID
			continue
		}
		if resp.StatusCode >= 300 {
			return fmt.Errorf("远程 Emby 请求失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(data, out)
	}
	return errors.New("远程 Emby 认证重试失败")
}

// TestConnection 连通性测试：确保地址可达且凭据有效；成功时回写自动认证信息。
func (r *EmbyRemoteService) TestConnection(ctx context.Context, acct *model.StrmAccount) error {
	cfg, err := r.configOf(acct)
	if err != nil {
		return err
	}
	if err := r.ensureToken(ctx, acct, cfg); err != nil {
		return err
	}
	var out json.RawMessage
	return r.doGet(ctx, acct, cfg, "/System/Info", nil, &out)
}

// ProxyPlayOf 返回账号是否配置了播放代理（供账号列表/编辑回显）。
func (r *EmbyRemoteService) ProxyPlayOf(acct *model.StrmAccount) (bool, error) {
	cfg, err := r.configOf(acct)
	if err != nil {
		return false, err
	}
	return cfg.ProxyPlay, nil
}

// ─── 元数据 / 目录聚合 ─────────────────────────────────────────────────────────

// RemoteViews 拉取远程媒体库（View）列表，返回远程原始 view map（未重写）。
func (r *EmbyRemoteService) RemoteViews(ctx context.Context, acct *model.StrmAccount) ([]map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	cacheKey := r.remoteCacheKey("views", acct.ID, r.remoteUserID(cfg))
	var cached []map[string]any
	if r.cache != nil && r.cache.GetJSON(ctx, cacheKey, &cached) {
		return cached, nil
	}
	q := url.Values{"api_key": {cfg.Token}}
	var body struct {
		Items []map[string]any `json:"Items"`
	}
	if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Views", q, &body); err != nil {
		return nil, err
	}
	if body.Items == nil {
		body.Items = []map[string]any{}
	}
	if r.cache != nil {
		r.cache.SetJSON(ctx, cacheKey, body.Items, r.remoteMediaCacheTTL())
	}
	return body.Items, nil
}

func (r *EmbyRemoteService) remoteUserID(cfg *EmbyRemoteConfig) string {
	if strings.TrimSpace(cfg.RemoteUserID) != "" {
		return cfg.RemoteUserID
	}
	return "0" // 未认证出的兜底：部分 Emby 接受 0 代表管理员
}

// RemoteItems 向远程 Emby 转发 /Items 浏览/搜索请求，返回重写后的响应载荷。
// p 的分页/排序/过滤参数原样转发，分页语义完全由远程承接。
func (r *EmbyRemoteService) RemoteItems(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, p ItemsParams) (map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	_, remoteParent, _ := DecodeEmbyRemoteID(p.ParentID)
	q := url.Values{}
	if remoteParent != "" {
		q.Set("ParentId", remoteParent)
	}
	q.Set("UserId", r.remoteUserID(cfg))
	q.Set("Limit", strconv.Itoa(p.Limit))
	q.Set("StartIndex", strconv.Itoa(p.StartIndex))
	if p.SearchTerm != "" {
		q.Set("SearchTerm", p.SearchTerm)
	}
	if p.Recursive {
		q.Set("Recursive", "true")
	}
	if p.SortBy != "" {
		q.Set("SortBy", p.SortBy)
	}
	if p.SortOrder != "" {
		q.Set("SortOrder", p.SortOrder)
	}
	if len(p.IncludeItemTypes) > 0 {
		q.Set("IncludeItemTypes", strings.Join(p.IncludeItemTypes, ","))
	}
	if len(p.Filters) > 0 {
		q.Set("Filters", strings.Join(p.Filters, ","))
	}
	path := "/Users/" + url.PathEscape(r.remoteUserID(cfg)) + "/Items"
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, path, q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": p.StartIndex}
	}
	RewriteEmbyRemoteIDs(out, mount.ID)
	return out, nil
}

// RemoteSearchMount 对单个挂载的媒体库执行全局搜索（ParentId=挂载的远程库，
// Recursive 返回库内全部命中），结果归属明确可直接伪装。
func (r *EmbyRemoteService) RemoteSearchMount(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, p ItemsParams) (map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("ParentId", mount.RemoteViewID)
	q.Set("Recursive", "true")
	q.Set("SearchTerm", p.SearchTerm)
	q.Set("UserId", r.remoteUserID(cfg))
	q.Set("Limit", strconv.Itoa(p.Limit))
	q.Set("StartIndex", strconv.Itoa(p.StartIndex))
	if p.SortBy != "" {
		q.Set("SortBy", p.SortBy)
	}
	if p.SortOrder != "" {
		q.Set("SortOrder", p.SortOrder)
	}
	if len(p.IncludeItemTypes) > 0 {
		q.Set("IncludeItemTypes", strings.Join(p.IncludeItemTypes, ","))
	}
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, "/Users/"+url.PathEscape(r.remoteUserID(cfg))+"/Items", q, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{"Items": []any{}, "TotalRecordCount": 0}
	}
	RewriteEmbyRemoteIDs(out, mount.ID)
	return out, nil
}

// RemoteItem 拉取远程单条目详情（含响应的重写）。
func (r *EmbyRemoteService) RemoteItem(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteID string) (map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	path := "/Users/" + url.PathEscape(r.remoteUserID(cfg)) + "/Items/" + url.PathEscape(remoteID)
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, path, nil, &out); err != nil {
		return nil, err
	}
	RewriteEmbyRemoteIDs(out, mount.ID)
	return out, nil
}

// RemoteLatest 拉取远程「最近添加」（用于 /Items/Latest 聚合）。
func (r *EmbyRemoteService) RemoteLatest(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, parentID string, limit int) ([]map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	q := url.Values{"Limit": {strconv.Itoa(limit)}}
	if parentID != "" {
		q.Set("ParentId", parentID)
	}
	path := "/Users/" + url.PathEscape(r.remoteUserID(cfg)) + "/Items/Latest"
	var out []map[string]any
	if err := r.doGet(ctx, acct, cfg, path, q, &out); err != nil {
		return nil, err
	}
	RewriteEmbyRemoteIDs(out, mount.ID)
	return out, nil
}

// RemotePlaybackInfo 拉取远程 PlaybackInfo，并按挂载的 proxy_play 配置重写
// 播放 URL：不代理=指向远程绝对地址（播放字节不过 MeBox）；代理=指向 MeBox
// 本地 /Videos/{encodedID} 端点（由 ProxyVideoStream 反代）。
func (r *EmbyRemoteService) RemotePlaybackInfo(ctx context.Context, mount *model.EmbyMount, acct *model.StrmAccount, remoteID, userID string) (map[string]any, error) {
	cfg, err := r.remoteConfigWithToken(ctx, acct)
	if err != nil {
		return nil, err
	}
	q := url.Values{"UserId": {r.remoteUserID(cfg)}}
	path := "/Items/" + url.PathEscape(remoteID) + "/PlaybackInfo"
	var out map[string]any
	if err := r.doGet(ctx, acct, cfg, path, q, &out); err != nil {
		return nil, err
	}
	RewriteEmbyRemoteIDs(out, mount.ID)
	r.rewritePlayURLs(out, mount, cfg, remoteID)
	return out, nil
}

// rewritePlayURLs 按挂载代理模式重写载荷内 MediaSources 的播放地址。
// 远程 Emby 的 PlaybackInfo 通常不返回 DirectStreamUrl（客户端靠它拼
// /Videos/{Id}/stream），因此这里总是强制构造播放地址，完全由 MeBox 掌控
// 直连（远程绝对 URL）或代理（本地 /Videos/{encoded}）的最终去向。
func (r *EmbyRemoteService) rewritePlayURLs(value any, mount *model.EmbyMount, cfg *EmbyRemoteConfig, remoteID string) {
	encoded := EncodeEmbyRemoteID(mount.ID, remoteID)
	sources := collectMediaSources(value)
	if sources == nil {
		return
	}
	for _, src := range sources {
		mediaSourceID, _ := src["Id"].(string)
		var streamPath, subtitlePlayURL string
		if mount.ProxyPlay {
			streamPath = "/Videos/" + url.PathEscape(encoded) + "/stream"
			subtitlePlayURL = "/Videos/" + url.PathEscape(encoded)
		} else {
			base := r.embyBase(cfg)
			streamPath = base + "/Videos/" + url.PathEscape(remoteID) + "/stream?api_key=" + url.QueryEscape(cfg.Token) + "&Static=true"
			if mediaSourceID != "" {
				streamPath += "&MediaSourceId=" + url.QueryEscape(mediaSourceID)
			}
			subtitlePlayURL = base + "/Videos/" + url.PathEscape(remoteID)
		}
		// 直连/代理地址总是下发（PlaybackInfo 语义：客户端直接请求该 URL）。
		src["DirectStreamUrl"] = streamPath
		if _, exists := src["TranscodingUrl"]; exists {
			src["TranscodingUrl"] = streamPath
		}
		rewriteSubtitleDeliveryURLs(src, subtitlePlayURL, cfg)
	}
}

// collectMediaSources 从载荷中取出所有 MediaSources（顶层或嵌套 Items 内）。
func collectMediaSources(value any) []map[string]any {
	var out []map[string]any
	switch typed := value.(type) {
	case map[string]any:
		if sources, ok := typed["MediaSources"].([]map[string]any); ok {
			out = append(out, sources...)
		} else if sources, ok := typed["MediaSources"].([]any); ok {
			for _, s := range sources {
				if m, isMap := s.(map[string]any); isMap {
					out = append(out, m)
				}
			}
		}
		if items, ok := typed["Items"]; ok {
			out = append(out, collectMediaSources(items)...)
		}
	case []any:
		for _, item := range typed {
			out = append(out, collectMediaSources(item)...)
		}
	case []map[string]any:
		for _, item := range typed {
			out = append(out, collectMediaSources(item)...)
		}
	}
	return out
}

var embySubtitleDeliveryRE = regexp.MustCompile(`/Subtitles/(\d+)/Stream(\.[A-Za-z0-9]+)?`)

// rewriteSubtitleDeliveryURLs 把 MediaSource 内字幕轨道的 DeliveryUrl 改写到
// subtitlePlayURL 前缀（客户端请求本地代理端点 / 远程绝对地址）。
func rewriteSubtitleDeliveryURLs(src map[string]any, playURL string, cfg *EmbyRemoteConfig) {
	if src == nil {
		return
	}
	streams, ok := src["MediaStreams"].([]any)
	if !ok {
		return
	}
	for _, s := range streams {
		stream, isMap := s.(map[string]any)
		if !isMap || stream["Type"] != "Subtitle" {
			continue
		}
		raw, _ := stream["DeliveryUrl"].(string)
		if raw == "" {
			continue
		}
		idx := "1"
		if m := embySubtitleDeliveryRE.FindStringSubmatch(raw); len(m) >= 2 {
			idx = m[1]
		}
		ext := ""
		if m := embySubtitleDeliveryRE.FindStringSubmatch(raw); len(m) >= 3 {
			ext = m[2]
		}
		base := strings.TrimRight(playURL, "/")
		delivery := base + "/Subtitles/" + idx + "/Stream" + ext
		if !cfg.ProxyPlay && strings.TrimSpace(cfg.Token) != "" {
			delivery += "?api_key=" + url.QueryEscape(cfg.Token)
		}
		stream["DeliveryUrl"] = delivery
	}
}

// RemoteImageURL 构造远程图片绝对地址（由既有 ImageProxy 拉取透传）。
func (r *EmbyRemoteService) RemoteImageURL(ctx context.Context, acct *model.StrmAccount, remoteID, imageType string) (string, error) {
	cfg, err := r.configOf(acct)
	if err != nil {
		return "", err
	}
	return r.embyBase(cfg) + "/Items/" + url.PathEscape(remoteID) + "/Images/" + url.PathEscape(strings.ToLower(imageType)) +
		"?api_key=" + url.QueryEscape(cfg.Token), nil
}

// ─── 播放代理 ─────────────────────────────────────────────────────────────────

// ProxyVideoStream 反向代理远程 Emby 视频流（保留 Range 以支持拖动）。
func (r *EmbyRemoteService) ProxyVideoStream(ctx context.Context, w http.ResponseWriter, req *http.Request, acct *model.StrmAccount, remoteID string) error {
	cfg, err := r.configOf(acct)
	if err != nil {
		return err
	}
	var lastErr error
	for _, lineIdx := range r.lineOrder(cfg) {
		lineCfg := r.withLine(cfg, lineIdx)
		lineCfg.Token = cfg.Token
		lineCfg.RemoteUserID = cfg.RemoteUserID
		if strings.TrimSpace(lineCfg.Token) == "" {
			if err := r.ensureToken(ctx, acct, lineCfg); err != nil {
				lastErr = err
				if isEmbyLineFailoverError(err) {
					continue
				}
				return err
			}
			cfg.Token = lineCfg.Token
			cfg.RemoteUserID = lineCfg.RemoteUserID
		}
		err := r.proxyVideoStreamOnLine(ctx, w, req, lineCfg, remoteID)
		if err == nil {
			r.adoptWorkingLine(ctx, acct, cfg, lineIdx)
			return nil
		}
		lastErr = err
		if !isEmbyLineFailoverError(err) {
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("远程 Emby 视频流请求失败")
}

func (r *EmbyRemoteService) proxyVideoStreamOnLine(ctx context.Context, w http.ResponseWriter, req *http.Request, cfg *EmbyRemoteConfig, remoteID string) error {
	endpoint := r.embyBase(cfg) + "/Videos/" + url.PathEscape(remoteID) + "/stream"
	q := url.Values{}
	if mediaSourceID := strings.TrimSpace(req.URL.Query().Get("MediaSourceId")); mediaSourceID != "" {
		q.Set("MediaSourceId", mediaSourceID)
	}
	// 代理是纯 byte 中继：始终要求远程原文件直连（Static=true 阻止远程触发
	// ffmpeg 转码调度——远程转码可能未配置/故障，导致整个代理 500）。
	q.Set("Static", "true")
	q.Set("api_key", cfg.Token)
	if encoded := q.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	upstream.Header.Set("X-Emby-Token", cfg.Token)
	if rangeHeader := req.Header.Get("Range"); rangeHeader != "" {
		upstream.Header.Set("Range", rangeHeader)
	}
	resp, err := r.stream.Do(upstream)
	if err != nil {
		return fmt.Errorf("连接远程 Emby 视频流失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("远程 Emby 视频流失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Cache-Control"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK {
		w.WriteHeader(resp.StatusCode)
	} else {
		w.WriteHeader(resp.StatusCode)
	}
	if resp.StatusCode == http.StatusPartialContent || resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(w, resp.Body)
	}
	return nil
}

// ProxySubtitle 反向代理远程 Emby 字幕流。
func (r *EmbyRemoteService) ProxySubtitle(ctx context.Context, w http.ResponseWriter, req *http.Request, acct *model.StrmAccount, remoteID, index string) error {
	cfg, err := r.configOf(acct)
	if err != nil {
		return err
	}
	var lastErr error
	for _, lineIdx := range r.lineOrder(cfg) {
		lineCfg := r.withLine(cfg, lineIdx)
		lineCfg.Token = cfg.Token
		lineCfg.RemoteUserID = cfg.RemoteUserID
		if strings.TrimSpace(lineCfg.Token) == "" {
			if err := r.ensureToken(ctx, acct, lineCfg); err != nil {
				lastErr = err
				if isEmbyLineFailoverError(err) {
					continue
				}
				return err
			}
			cfg.Token = lineCfg.Token
			cfg.RemoteUserID = lineCfg.RemoteUserID
		}
		err := r.proxySubtitleOnLine(ctx, w, lineCfg, remoteID, index)
		if err == nil {
			r.adoptWorkingLine(ctx, acct, cfg, lineIdx)
			return nil
		}
		lastErr = err
		if !isEmbyLineFailoverError(err) {
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("远程 Emby 字幕流请求失败")
}

func (r *EmbyRemoteService) proxySubtitleOnLine(ctx context.Context, w http.ResponseWriter, cfg *EmbyRemoteConfig, remoteID, index string) error {
	endpoint := r.embyBase(cfg) + "/Videos/" + url.PathEscape(remoteID) + "/Subtitles/" + url.PathEscape(index) + "/Stream"
	endpoint += "?api_key=" + url.QueryEscape(cfg.Token)
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	upstream.Header.Set("X-Emby-Token", cfg.Token)
	resp, err := r.stream.Do(upstream)
	if err != nil {
		return fmt.Errorf("连接远程 Emby 字幕流失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("远程 Emby 字幕流失败(%d)", resp.StatusCode)
	}
	if value := resp.Header.Get("Content-Type"); value != "" {
		w.Header().Set("Content-Type", value)
	}
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(w, resp.Body)
	}
	return nil
}

// ─── 播放状态透传 ──────────────────────────────────────────────────────────────

// ProxySetPlayed 把「已看/未看」状态透传到远程 Emby（MeBox 本地不落库）。
func (r *EmbyRemoteService) ProxySetPlayed(ctx context.Context, acct *model.StrmAccount, remoteID string, played bool) error {
	cfg, err := r.configOf(acct)
	if err != nil {
		return err
	}
	if err := r.ensureToken(ctx, acct, cfg); err != nil {
		return err
	}
	method := http.MethodPost
	path := "/Users/" + url.PathEscape(r.remoteUserID(cfg)) + "/PlayedItems/" + url.PathEscape(remoteID)
	if !played {
		method = http.MethodDelete
	}
	return r.doMutate(ctx, acct, cfg, method, path)
}

// ProxySetFavorite 把「收藏/取消收藏」状态透传到远程 Emby。
func (r *EmbyRemoteService) ProxySetFavorite(ctx context.Context, acct *model.StrmAccount, remoteID string, favorite bool) error {
	cfg, err := r.configOf(acct)
	if err != nil {
		return err
	}
	if err := r.ensureToken(ctx, acct, cfg); err != nil {
		return err
	}
	method := http.MethodPost
	path := "/Users/" + url.PathEscape(r.remoteUserID(cfg)) + "/FavoriteItems/" + url.PathEscape(remoteID)
	if !favorite {
		method = http.MethodDelete
	}
	return r.doMutate(ctx, acct, cfg, method, path)
}

func (r *EmbyRemoteService) doMutate(ctx context.Context, acct *model.StrmAccount, cfg *EmbyRemoteConfig, method, path string) error {
	var lastErr error
	for _, lineIdx := range r.lineOrder(cfg) {
		lineCfg := r.withLine(cfg, lineIdx)
		lineCfg.Token = cfg.Token
		lineCfg.RemoteUserID = cfg.RemoteUserID
		if strings.TrimSpace(lineCfg.Token) == "" {
			if err := r.ensureToken(ctx, acct, lineCfg); err != nil {
				lastErr = err
				if isEmbyLineFailoverError(err) {
					continue
				}
				return err
			}
			cfg.Token = lineCfg.Token
			cfg.RemoteUserID = lineCfg.RemoteUserID
		}
		err := r.doMutateOnLine(ctx, lineCfg, method, path)
		if err == nil {
			r.adoptWorkingLine(ctx, acct, cfg, lineIdx)
			return nil
		}
		lastErr = err
		if !isEmbyLineFailoverError(err) {
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("远程 Emby 状态同步失败")
}

func (r *EmbyRemoteService) doMutateOnLine(ctx context.Context, cfg *EmbyRemoteConfig, method, path string) error {
	endpoint := r.embyBase(cfg) + path + "?api_key=" + url.QueryEscape(cfg.Token)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", cfg.Token)
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求远程 Emby 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("远程 Emby 状态同步失败(%d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
