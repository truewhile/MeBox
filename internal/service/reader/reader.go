// Package reader — 阅读子系统服务：书源导入管理、搜索、详情、目录、正文。
// 业务语义对齐 legado 的 WebBook / SearchModel / BookSourceDebugModel。
package reader

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service/reader/rule"
)

const (
	perSourceTimeout   = 30 * time.Second
	searchConcurrency  = 8
	maxContentNextPage = 50 // 正文 nextContentUrl 翻页上限，防死循环
	maxBodyBytes       = 8 << 20
)

// ReaderService 阅读服务。
type ReaderService struct {
	cfg    *config.Config
	log    *zap.Logger
	repo   *repository.ReaderRepository
	http   *http.Client
	crypto *helper.SecretCipher

	// browserMu / browserPending 保护「待用户完成的页面」表
	// （java.startBrowser / startBrowserAwait，见 browser_panel.go）。
	browserMu      sync.Mutex
	browserPending map[string]*pendingBrowser
}

// NewReaderService 创建服务。
func NewReaderService(cfg *config.Config, log *zap.Logger, repos *repository.Container) *ReaderService {
	return &ReaderService{
		cfg:    cfg,
		log:    log,
		repo:   repos.Reader,
		http:   helper.NewSiteHTTPClient(30, true),
		crypto: helper.NewSecretCipher(firstNonEmpty(cfg.Secrets.EncryptionKey, cfg.Secrets.JWTSecret)),
	}
}

// ─── 书源导入与管理 ─────────────────────────────────────────────────────────

// ImportSources 导入书源：支持 JSON 数组 / 单对象 / Base64 / 网络URL。
// 返回导入数量。
func (s *ReaderService) ImportSources(ctx context.Context, text string) (int, error) {
	text = strings.TrimSpace(text)
	// 去 UTF-8 BOM（Windows 记事本导出的书源文件常见），否则 URL 检测和 JSON 解析都会失败
	text = strings.TrimPrefix(text, "\uFEFF")
	if text == "" {
		return 0, fmt.Errorf("导入内容为空")
	}
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		body, _, _, err := s.execute(ctx, &rule.Request{Method: "GET", URL: text, URLNoQuery: text, Headers: map[string]string{}})
		if err != nil {
			return 0, fmt.Errorf("拉取书源失败: %w", err)
		}
		text = body
	}
	sources := ParseSourcePayload(text)
	if len(sources) == 0 {
		return 0, fmt.Errorf("未识别到有效书源（支持 JSON 数组/对象或 Base64）")
	}
	imported := 0
	for _, raw := range sources {
		bs, err := ParseBookSource(raw)
		if err != nil || bs.BookSourceURL == "" {
			continue
		}
		// 已存在则更新，否则新建（按书源 URL 去重）
		existing, err := s.repo.GetSourceByURL(ctx, bs.BookSourceURL)
		now := time.Now()
		record := &model.ReaderBookSource{
			Name:           bs.BookSourceName,
			GroupName:      strings.TrimSpace(SPtr(bs.BookSourceGroup)),
			Type:           bs.Type(),
			SourceURL:      bs.BookSourceURL,
			RawJSON:        raw,
			Enabled:        bs.Enabled == nil || *bs.Enabled,
			EnabledExplore: bs.EnabledExplore == nil || *bs.EnabledExplore,
			CustomOrder:    IPtr(bs.CustomOrder),
			Weight:         IPtr(bs.Weight),
			ConcurrentRate: SPtr(bs.ConcurrentRate),
			Header:         SPtr(bs.Header),
			Comment:        SPtr(bs.BookSourceComment),
			Variables:      SPtr(bs.RawVariables),
			LastUpdateTime: int64Now(bs.LastUpdateTime),
		}
		if existing != nil {
			existing.Name = record.Name
			existing.GroupName = record.GroupName
			existing.Type = record.Type
			existing.RawJSON = record.RawJSON
			existing.Enabled = record.Enabled
			existing.EnabledExplore = record.EnabledExplore
			existing.CustomOrder = record.CustomOrder
			existing.Weight = record.Weight
			existing.ConcurrentRate = record.ConcurrentRate
			existing.Header = record.Header
			existing.Comment = record.Comment
			existing.Variables = record.Variables
			existing.LastUpdateTime = record.LastUpdateTime
			existing.LastCheckAt = &now
			if err := s.repo.UpdateSource(ctx, existing); err == nil {
				imported++
			}
			continue
		}
		record.LastCheckAt = &now
		if err := s.repo.CreateSource(ctx, record); err == nil {
			imported++
		}
	}
	return imported, nil
}

func int64Now(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// ParseSourcePayload 识别 JSON 数组 / 单对象 / Base64 / 每行一个对象，
// 返回书源 JSON 字符串列表（冒烟 CLI 复用）。
func ParseSourcePayload(text string) []string {
	text = strings.TrimPrefix(strings.TrimSpace(text), "\uFEFF")
	tryDecode := func(s string) []string {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			var out []string
			for _, item := range arr {
				out = append(out, string(item))
			}
			return out
		}
		var single json.RawMessage
		if err := json.Unmarshal([]byte(s), &single); err == nil {
			if len(single) > 0 && single[0] == '[' {
				return nil
			}
			return []string{string(single)}
		}
		return nil
	}
	if out := tryDecode(text); out != nil {
		return out
	}
	// Base64（含无 padding / URL-safe 变体）
	if cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' {
			return -1
		}
		return r
	}, text); !strings.HasPrefix(cleaned, "{") {
		if decoded, err := base64.StdEncoding.DecodeString(cleaned); err == nil && json.Valid(decoded) {
			if out := tryDecode(string(decoded)); out != nil {
				return out
			}
		}
		if decoded, err := base64.RawStdEncoding.DecodeString(cleaned); err == nil && json.Valid(decoded) {
			if out := tryDecode(string(decoded)); out != nil {
				return out
			}
		}
	}
	// 每行一个对象
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if items := tryDecode(line); items != nil {
			out = append(out, items...)
		}
	}
	return out
}

// ListSources 书源列表（标注是否支持登录，供前端决定是否显示登录入口）。
func (s *ReaderService) ListSources(ctx context.Context) ([]model.ReaderBookSource, error) {
	sources, err := s.repo.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		sources[i].HasLogin = rawSourceHasLogin(sources[i].RawJSON)
	}
	return sources, nil
}

// rawSourceHasLogin 只解出 loginUrl/loginUi 两个字段判断登录能力。
// 列表接口按需计算，避免为了一个布尔值把每个书源的完整 JSON 都反序列化。
func rawSourceHasLogin(rawJSON string) bool {
	var probe struct {
		LoginURL *string `json:"loginUrl"`
		LoginUI  *string `json:"loginUi"`
	}
	if json.Unmarshal([]byte(rawJSON), &probe) != nil {
		return false
	}
	return strings.TrimSpace(SPtr(probe.LoginURL)) != "" || strings.TrimSpace(SPtr(probe.LoginUI)) != ""
}

// UpdateSourceEnabled 启停书源。
func (s *ReaderService) UpdateSourceEnabled(ctx context.Context, id string, enabled bool) error {
	src, err := s.repo.GetSource(ctx, id)
	if err != nil {
		return err
	}
	src.Enabled = enabled
	return s.repo.UpdateSource(ctx, src)
}

// DeleteSource 删除书源。
func (s *ReaderService) DeleteSource(ctx context.Context, id string) error {
	return s.repo.DeleteSource(ctx, id)
}

// ─── HTTP 执行 ──────────────────────────────────────────────────────────────

// execute 执行 rule.Request，返回（解码后 body, 最终 URL, HTTP 状态码）。
func (s *ReaderService) execute(ctx context.Context, req *rule.Request) (string, string, int, error) {
	return s.executeWithState(ctx, req, nil, false)
}

// executeWithState 在 execute 基础上叠加会话：附加 Cookie / loginHeader，
// 并在 captureCookies 为真时把响应 Set-Cookie 回写到会话。
func (s *ReaderService) executeWithState(ctx context.Context, req *rule.Request, state *sourceState, captureCookies bool) (string, string, int, error) {
	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}
	// 请求目标是含 query 的完整 URL：对应 legado 的 `get(urlNoQuery, encodedQuery)`
	// （两端拼起来才是最终地址）。ParseAnalyzeUrl 已把重编码后的 query 放进
	// req.URL。早期这里误用 URLNoQuery，导致所有「参数写在 query 里」的 GET
	// 请求都丢掉了参数（搜索关键词、分页等），下游站点拿到空参数直接返回空结果。
	target := req.URL
	if target == "" {
		target = req.URLNoQuery
	}
	// data: 地址是书源自带的「参数信封」，内容在本地，不发网络请求。
	// 对应 legado AnalyzeUrl.getByteArrayIfDataUri()。
	if raw, ok := rule.DecodeDataURI(target); ok {
		return rule.EncodeRuleBody(req, raw, req.Charset), target, http.StatusOK, nil
	}
	if state != nil {
		// 登录请求头（除 Cookie 外）优先级低于书源显式配置，高于预设。
		for k, v := range state.LoginHeaderMap() {
			if strings.EqualFold(k, "cookie") {
				continue
			}
			if _, ok := req.Headers[k]; !ok {
				req.Headers[k] = v
			}
		}
		// Cookie 仅在调用方未显式指定时附加，避免覆盖书源自带的鉴权 Cookie。
		if hasHeaderFold(req.Headers, "cookie") == "" {
			if ck := state.CookieForRequest(target); ck != "" {
				req.Headers["Cookie"] = ck
			}
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, target, bodyReader)
	if err != nil {
		return "", "", 0, err
	}
	for k, v := range helper.HTTPHeaderPresets() {
		httpReq.Header.Set(k, v)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	// Accept-Encoding 必须留给 net/http：只有调用方没设置时它才会自动解压，
	// 否则压缩响应会以原始字节进入规则层（书源的 JSON.parse 会直接炸）。
	// 书源 JSON 的 header 字段也可能塞了这个头，所以放在最后统一清掉。
	helper.StripAcceptEncoding(httpReq.Header)
	if req.Method == "POST" {
		switch {
		case req.IsForm:
			httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		case req.IsJSON:
			httpReq.Header.Set("Content-Type", "application/json")
		}
	}
	resp, err := s.http.Do(httpReq)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", "", resp.StatusCode, err
	}
	data = helper.DecompressBody(resp, data)
	// 声明了 type 的请求按「原始字节的 hex」返回（对应 legado AnalyzeUrl.type），
	// 不做 charset 解码——书源会自己 hexDecodeToString 取回内容。
	if req.HexBody {
		return hex.EncodeToString(data), resp.Request.URL.String(), resp.StatusCode, nil
	}
	charset := req.Charset
	if charset == "" {
		charset = charsetFromContentType(resp.Header.Get("Content-Type"))
	}
	body, err := rule.DecodeBytes(data, charset)
	if err != nil {
		body = string(data)
	}
	finalURL := resp.Request.URL.String()
	// 记录 Set-Cookie（书源 JS 的 cookie.getCookie 可读取）
	var cookieStrs []string
	for _, ck := range resp.Cookies() {
		cookieStrs = append(cookieStrs, ck.Name+"="+ck.Value)
	}
	if len(cookieStrs) > 0 && state != nil && captureCookies {
		for _, ck := range cookieStrs {
			state.SetCookie(finalURL, ck)
		}
	}
	// bodyJs 二次处理
	if req.BodyJsFn != nil {
		body = req.BodyJsFn(body)
	}
	if strings.EqualFold(charsetFromContentType(resp.Header.Get("Content-Type")), "xml") &&
		!strings.HasPrefix(strings.TrimSpace(body), "<?xml") {
		body = "<?xml version=\"1.0\"?>" + body
	}
	return body, finalURL, resp.StatusCode, nil
}

// hasHeaderFold 大小写不敏感地取请求头值。
func hasHeaderFold(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func charsetFromContentType(ct string) string {
	if ct == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(ct), "charset=")
	if idx == -1 {
		return ""
	}
	cs := strings.TrimSpace(ct[idx+len("charset="):])
	if i := strings.Index(cs, ";"); i >= 0 {
		cs = cs[:i]
	}
	return strings.Trim(cs, "\"")
}

// ─── 规则执行辅助 ───────────────────────────────────────────────────────────

// sourceSession 是一次书源操作的执行上下文：把书源记录、解析结构与
// 会话状态（Cookie/变量/登录信息）绑在一起，供各阶段复用并在结束时落库。
type sourceSession struct {
	svc   *ReaderService
	ctx   context.Context
	src   *model.ReaderBookSource
	bs    *BookSource
	state *sourceState
	// userID 触发本次操作的用户（登录界面的浏览器待办按用户隔离）。
	userID string
	// browserEnabled 是否给本次执行注入宿主浏览器。
	// 只在登录动作里开启：startBrowserAwait 会阻塞等待用户操作（可达十分钟），
	// 若在搜索/正文等链路里被书源意外调用，会把普通请求长时间挂住。
	browserEnabled bool
}

// newSession 为指定书源建立执行上下文。
func (s *ReaderService) newSession(ctx context.Context, src *model.ReaderBookSource, bs *BookSource) *sourceSession {
	url := ""
	if src != nil {
		url = src.SourceURL
	} else if bs != nil {
		url = bs.BookSourceURL
	}
	sess := &sourceSession{svc: s, ctx: ctx, src: src, bs: bs, state: s.newSourceState(ctx, url)}
	sess.seedVariable()
	return sess
}

// seedVariable 用书源 JSON 的 variables 字段初始化源变量（仅当尚未保存过）。
// 对应「导入书源后源变量为作者设定的默认值」的行为。
func (sess *sourceSession) seedVariable() {
	if sess.bs == nil || sess.state == nil {
		return
	}
	if strings.TrimSpace(sess.state.GetVariable()) != "" {
		return
	}
	if raw := strings.TrimSpace(SPtr(sess.bs.RawVariables)); raw != "" && json.Valid([]byte(raw)) {
		sess.state.SetVariable(raw)
	}
}

// close 落库会话状态（Cookie/变量可能在执行中被书源 JS 改写）。
func (sess *sourceSession) close() {
	if sess != nil && sess.state != nil {
		sess.state.flush()
	}
}

// fetch 执行请求，并按书源配置决定是否自动保存响应里的 Cookie。
func (sess *sourceSession) fetch(req *rule.Request) (string, string, int, error) {
	body, finalURL, code, err := sess.svc.executeWithState(sess.ctx, req, sess.state, sess.captureCookies())
	if err != nil {
		return body, finalURL, code, err
	}
	// loginCheckJs：书源借此检测会话失效并自行重登/重取（对应 legado WebBook.checkJs）。
	// 返回新 body 时替换原响应，使上层规则直接拿到修复后的内容。
	// 声明了 type 的响应是原始字节的 hex（参数信封），不是可校验的文本，跳过。
	if check := SPtr(sess.bs.LoginCheckJS); strings.TrimSpace(check) != "" && !req.HexBody {
		body = sess.applyLoginCheck(check, body, code, finalURL)
	}
	return body, finalURL, code, nil
}

// captureCookies 是否自动保存响应里的 Set-Cookie。
// 对应 legado 的 enabledCookieJar（默认 true）：关掉后不再自动累积 Cookie，
// 但书源 JS 主动 cookie.setCookie 写入的仍会保存（那是明确意图）。
func (sess *sourceSession) captureCookies() bool {
	return sess.bs == nil || sess.bs.EnabledCookieJarOrDefault()
}

// applyLoginCheck 执行 loginCheckJs；失败时保留原 body，避免因检查脚本本身出错而中断阅读。
func (sess *sourceSession) applyLoginCheck(check, body string, code int, finalURL string) string {
	runner := sess.runner("", 0)
	newBody, changed, err := runner.EvalLoginCheck(stripJSWrapper(check), body, code, finalURL)
	if err != nil {
		if sess.svc.log != nil {
			sess.svc.log.Warn("reader:loginCheckJs 执行失败",
				zap.String("source", sess.srcName()), zap.Error(err))
		}
		return body
	}
	if changed && newBody != "" {
		return newBody
	}
	return body
}

// newAnalyzer 构建规则解析器（注入书源变量与 JS 运行时）。
func (sess *sourceSession) newAnalyzer(key string, page int, body, finalURL string) *rule.AnalyzeRule {
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	applySourceVariables(ar, sess.bs)
	// 内嵌 JS 里的 java.put/java.get 读写书源级变量（对应 source.variableMap）
	ar.SetSourceVariables(sess.state.GetVariableKey, sess.state.SetVariableKey)
	ar.SetJSRunner(sess.runner(key, page).ForAnalyzer(ar))
	return ar
}

// applyBookContext 把书籍/章节上下文注入解析器，供规则 JS 的 book / chapter 对象
// 读取（对应 legado 里 AnalyzeRule 持有 Book / BookChapter 实体）。
func (sess *sourceSession) applyBookContext(ar *rule.AnalyzeRule, bookURL string, book *model.ReaderBook, chapterTitle string, chapterIndex int) {
	meta := map[string]any{"bookUrl": bookURL, "tocUrl": bookURL}
	if book != nil {
		meta["name"] = book.Name
		meta["author"] = book.Author
		meta["origin"] = book.Origin
		meta["originName"] = book.OriginName
		meta["kind"] = book.Kind
		meta["coverUrl"] = book.CoverURL
		meta["intro"] = book.Intro
		meta["type"] = book.Type
		meta["order"] = book.Order
		meta["durChapterIndex"] = book.DurChapterIndex
		meta["durChapterTitle"] = book.DurChapterTitle
		meta["durChapterPos"] = book.DurChapterPos
		ar.SetBookContext(book.Name, nil)
		ar.SetBookCustomVars(parseBookVariableMap(book.Variable))
	}
	ar.SetBookMeta(meta)
	ar.SetChapterContext(chapterTitle, nil)
	ar.SetChapterIndex(chapterIndex)
}

// parseBookVariableMap 解析书架书籍的自定义变量 JSON（对应 legado Book.variableMap）。
func parseBookVariableMap(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// runner 构建本次请求的 JS 运行时（网络桥回 execute，携带书源上下文与会话状态）。
func (sess *sourceSession) runner(key string, page int) *rule.JSRunner {
	// 宿主浏览器只在登录动作里注入（见 browserEnabled 的说明）；
	// 其它链路保持「无浏览器」语义，java.startBrowser* 会如实报错。
	var host rule.BrowserHost
	if sess.browserEnabled {
		host = &browserHost{
			svc:       sess.svc,
			sourceURL: sess.srcURL(),
			sourceID:  sess.srcID(),
			userID:    sess.userID,
		}
	}
	return rule.NewJSRunner(rule.JSConfig{
		Fetch: func(req *rule.Request) (string, string, int, error) {
			return sess.fetch(req)
		},
		SourceProps: sess.bs.SourceProps(),
		Log: func(msg string) {
			if sess.svc.log != nil {
				sess.svc.log.Info("reader:source-js",
					zap.String("source", srcNameOf(sess.src, sess.bs)), zap.String("log", msg))
			}
		},
		BaseURL: sess.srcURL(),
		Key:     key,
		Page:    page,
		State:   sess.state,
		JSLib:   SPtr(sess.bs.JSLib),
		Ctx:     sess.ctx,
		Browser: host,
	})
}

// srcID 书源记录 ID（登录界面待办按书源隔离）。
func (sess *sourceSession) srcID() string {
	if sess.src != nil {
		return sess.src.ID
	}
	return ""
}

// srcURL 书源标识 URL（会话状态与 baseUrl 的键）。
func (sess *sourceSession) srcURL() string {
	if sess.src != nil && sess.src.SourceURL != "" {
		return sess.src.SourceURL
	}
	if sess.bs != nil {
		return sess.bs.BookSourceURL
	}
	return ""
}

// srcName 书源显示名。
func (sess *sourceSession) srcName() string {
	return srcNameOf(sess.src, sess.bs)
}

// headerJSON 书源级请求头 JSON。
func (sess *sourceSession) headerJSON() string {
	if sess.src != nil && sess.src.Header != "" {
		return sess.src.Header
	}
	return SPtr(sess.bs.Header)
}

// applyHeaders 把书源级请求头合并进请求（不覆盖已显式设置的值）。
func (sess *sourceSession) applyHeaders(req *rule.Request) {
	raw := sess.headerJSON()
	if raw == "" {
		return
	}
	var headers map[string]any
	if json.Unmarshal([]byte(raw), &headers) == nil {
		for k, v := range headers {
			if _, ok := req.Headers[k]; !ok {
				req.Headers[k] = fmt.Sprintf("%v", v)
			}
		}
	}
}

func srcNameOf(src *model.ReaderBookSource, bs *BookSource) string {
	if src != nil {
		return src.Name
	}
	if bs != nil {
		return bs.BookSourceName
	}
	return ""
}

// ─── 搜索 ──────────────────────────────────────────────────────────────────

// SearchBook 搜索结果项（对应 legado SearchBook）。
type SearchBook struct {
	Name          string        `json:"name"`
	Author        string        `json:"author"`
	Kind          string        `json:"kind"`
	WordCount     string        `json:"word_count"`
	LatestChapter string        `json:"latest_chapter"`
	Intro         string        `json:"intro"`
	CoverURL      string        `json:"cover_url"`
	BookURL       string        `json:"book_url"`
	Origins       []SearchOrigin `json:"origins"`
}

// SearchOrigin 命中该书目的书源（换源用）。
type SearchOrigin struct {
	SourceID   string `json:"source_id"`
	Origin     string `json:"origin"`
	OriginName string `json:"origin_name"`
	OriginType int    `json:"origin_type"`
	BookURL    string `json:"book_url"`
	LatestChapter string `json:"latest_chapter"`
}

type searchHit struct {
	book SearchBook
	tier int
}

// Search 多源聚合搜索（同步返回，P1 改为 WS 流式推送）。
func (s *ReaderService) Search(ctx context.Context, key string) ([]SearchBook, []SearchSkipped, error) {
	sources, err := s.repo.ListSources(ctx)
	if err != nil {
		return nil, nil, err
	}
	var enabled []model.ReaderBookSource
	for _, src := range sources {
		if src.Enabled {
			enabled = append(enabled, src)
		}
	}
	if len(enabled) == 0 {
		return nil, nil, fmt.Errorf("没有已启用的书源")
	}

	var (
		mu sync.Mutex
		// 初始化为空切片而不是 nil：nil 切片会被编码成 JSON null，
		// 前端一旦按数组用（skipped.length）就直接 TypeError 崩页面。
		hits    = []searchHit{}
		skipped = []SearchSkipped{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(searchConcurrency)
	for _, src := range enabled {
		src := src
		g.Go(func() error {
			gctxSrc, cancel := context.WithTimeout(gctx, perSourceTimeout)
			defer cancel()
			books, err := s.searchInSource(gctxSrc, &src, nil, key, 1)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				skipped = append(skipped, SearchSkipped{SourceID: src.ID, OriginName: src.Name, Reason: err.Error()})
				return nil // 单源失败不影响整体
			}
			for i := range books {
				hits = append(hits, searchHit{book: books[i]})
			}
			return nil
		})
	}
	_ = g.Wait()
	return mergeSearchResults(hits, key), skipped, nil
}

// SearchSkipped 搜索失败的书源与原因（书源管理调试用）。
type SearchSkipped struct {
	SourceID   string `json:"source_id"`
	OriginName string `json:"origin_name"`
	Reason     string `json:"reason"`
}

// tierFor 对应 legado SearchModel 的结果分档：
// 书名或作者等于关键词 > kind 含关键词 > 书名/作者含关键词 > 其他。
func tierFor(b *SearchBook, key string) int {
	lk := strings.ToLower(key)
	switch {
	case strings.EqualFold(b.Name, key) || strings.EqualFold(b.Author, key):
		return 0
	case strings.Contains(strings.ToLower(b.Kind), lk):
		return 1
	case strings.Contains(strings.ToLower(b.Name), lk) || strings.Contains(strings.ToLower(b.Author), lk):
		return 2
	default:
		return 3
	}
}

// mergeSearchResults 对应 mergeItems：同名同作者合并（多源记录），
// 档间按 tier 顺序，档内按书源数降序。
func mergeSearchResults(hits []searchHit, key string) []SearchBook {
	merged := map[string]*SearchBook{}
	for _, hit := range hits {
		b := hit.book
		if b.Name == "" {
			continue
		}
		k := b.Name + "|" + b.Author
		if existing, ok := merged[k]; ok {
			existing.Origins = append(existing.Origins, b.Origins...)
			continue
		}
		merged[k] = &b
	}
	out := make([]SearchBook, 0, len(merged))
	for _, b := range merged {
		sort.SliceStable(b.Origins, func(i, j int) bool {
			return b.Origins[i].OriginName < b.Origins[j].OriginName
		})
		out = append(out, *b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := tierFor(&out[i], key), tierFor(&out[j], key)
		if ti != tj {
			return ti < tj
		}
		return len(out[i].Origins) > len(out[j].Origins)
	})
	return out
}

// searchInSource 单源搜索（对应 WebBook.searchBook）。
func (s *ReaderService) searchInSource(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, key string, page int) ([]SearchBook, error) {
	if bs == nil {
		var err error
		bs, err = ParseBookSource(src.RawJSON)
		if err != nil {
			return nil, fmt.Errorf("书源 JSON 解析失败")
		}
	}
	searchURL := SPtr(bs.SearchURL)
	if searchURL == "" {
		return nil, fmt.Errorf("书源未配置搜索地址")
	}
	sr := bs.RuleSearch
	if sr == nil || SPtr(sr.BookList) == "" {
		return nil, fmt.Errorf("书源未配置搜索列表规则")
	}
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	runner := sess.runner(key, page)
	req, err := rule.ParseAnalyzeUrlWithJS(searchURL, key, page, sess.srcURL(), runner)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	sess.applyHeaders(req)
	body, finalURL, _, err := sess.fetch(req)
	if err != nil {
		return nil, err
	}
	ar := sess.newAnalyzer(key, page, body, finalURL)

	elements, err := ar.GetElements(SPtr(sr.BookList))
	if err != nil {
		return nil, err
	}
	var books []SearchBook
	for _, el := range elements {
		name, err := ar.GetString(SPtr(sr.Name), el, false)
		if err != nil {
			continue
		}
		bookURL, err := ar.GetString(SPtr(sr.BookURL), el, true)
		if err != nil || bookURL == "" {
			continue
		}
		author, _ := ar.GetString(SPtr(sr.Author), el, false)
		kind, _ := ar.GetString(SPtr(sr.Kind), el, false)
		cover, _ := ar.GetString(SPtr(sr.CoverURL), el, true)
		intro, _ := ar.GetString(SPtr(sr.Intro), el, false)
		lastChapter, _ := ar.GetString(SPtr(sr.LastChapter), el, false)
		wordCount, _ := ar.GetString(SPtr(sr.WordCount), el, false)
		books = append(books, SearchBook{
			Name:          name,
			Author:        author,
			Kind:          kind,
			WordCount:     wordCount,
			LatestChapter: lastChapter,
			Intro:         intro,
			CoverURL:      cover,
			BookURL:       bookURL,
			Origins: []SearchOrigin{{
				SourceID:      src.ID,
				Origin:        src.SourceURL,
				OriginName:    firstNonEmpty(src.Name, bs.BookSourceName),
				OriginType:    src.Type,
				BookURL:       bookURL,
				LatestChapter: lastChapter,
			}},
		})
	}
	return books, nil
}

func applySourceVariables(ar *rule.AnalyzeRule, bs *BookSource) {
	if bs.Variables == nil {
		return
	}
	vars := map[string]string{}
	for k, v := range bs.Variables {
		vars[k] = fmt.Sprintf("%v", v)
	}
	ar.SetBookContext("", vars)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ─── 详情 / 目录 / 正文 ─────────────────────────────────────────────────────

// BookInfo 书籍详情（对应 legado Book 信息页）。
type BookInfo struct {
	Name          string `json:"name"`
	Author        string `json:"author"`
	Kind          string `json:"kind"`
	WordCount     string `json:"word_count"`
	LatestChapter string `json:"latest_chapter"`
	Intro         string `json:"intro"`
	CoverURL      string `json:"cover_url"`
	TocURL        string `json:"toc_url"`
	BookURL       string `json:"book_url"`
}

// GetBookInfo 抓取书籍详情。
func (s *ReaderService) GetBookInfo(ctx context.Context, sourceID, sourceURL, bookURL string) (*BookInfo, error) {
	src, bs, err := s.loadSourceFlexible(ctx, sourceID, sourceURL)
	if err != nil {
		return nil, err
	}
	return s.getBookInfoFrom(ctx, src, bs, bookURL)
}

func (s *ReaderService) getBookInfoFrom(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, bookURL string) (*BookInfo, error) {
	bir := bs.RuleBookInfo
	if bir == nil {
		return nil, fmt.Errorf("书源未配置详情规则")
	}
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	runner := sess.runner("", 0)
	req, err := rule.ParseAnalyzeUrlWithJS(bookURL, "", 0, sess.srcURL(), runner)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	sess.applyHeaders(req)
	body, finalURL, _, err := sess.fetch(req)
	if err != nil {
		return nil, err
	}
	ar := sess.newAnalyzer("", 0, body, finalURL)
	sess.applyBookContext(ar, bookURL, nil, "", 0)

	info := &BookInfo{BookURL: bookURL, TocURL: bookURL}
	if initRule := SPtr(bir.Init); initRule != "" {
		// 对应 legado BookInfo.analyzeBookInfo：
		//   analyzeRule.setContent(analyzeRule.getElement(infoRule.init))
		// init 的结果整体成为后续字段的解析内容。这个源的 init 是
		// `<js>…</js>$.data` 组合规则：先请求详情接口，再用 $.data 取出对象，
		// 后面的 name / author / tocUrl 都在这个对象上求值。
		if initVal, err := ar.GetElement(initRule); err == nil && initVal != nil {
			ar.SetContent(initVal, finalURL)
		}
	}
	if v, err := ar.GetString(SPtr(bir.Name), nil, false); err == nil && v != "" {
		info.Name = v
	}
	if v, err := ar.GetString(SPtr(bir.Author), nil, false); err == nil && v != "" {
		info.Author = v
	}
	if v, err := ar.GetString(SPtr(bir.Kind), nil, false); err == nil && v != "" {
		info.Kind = v
	}
	if v, err := ar.GetString(SPtr(bir.WordCount), nil, false); err == nil && v != "" {
		info.WordCount = v
	}
	if v, err := ar.GetString(SPtr(bir.LastChapter), nil, false); err == nil && v != "" {
		info.LatestChapter = v
	}
	if v, err := ar.GetString(SPtr(bir.Intro), nil, false); err == nil && v != "" {
		info.Intro = v
	}
	if v, err := ar.GetString(SPtr(bir.CoverURL), nil, true); err == nil && v != "" {
		info.CoverURL = v
	}
	if v, err := ar.GetString(SPtr(bir.TocURL), nil, true); err == nil && v != "" {
		info.TocURL = v
	}
	return info, nil
}

// TocChapter 目录章节项。
type TocChapter struct {
	Index     int    `json:"index"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	IsVolume  bool   `json:"is_volume"`
	UpdateTime string `json:"update_time"`
}

// normalizeBookType 把 legado BookType 的位掩码归一成 MeBox 内部使用的
// 0 文本 / 1 音频 / 2 图片 / 3 视频。
//
// legado 的 BookType 是按位区分的常量（4=视频、8=文本、32=音频、64=图片），
// 书源在目录规则里直接给 book.type 赋这些值（听书源写 32 表示音频），
// 而 MeBox 的 ReaderBook.Type 沿用书源 bookSourceType 的 0/1/2/3 约定，
// 两者必须转换，否则音频书会被当成文本来渲染。
func normalizeBookType(t int) int {
	switch {
	case t&32 != 0:
		return 1 // audio
	case t&64 != 0:
		return 2 // image
	case t&4 != 0:
		return 3 // video
	case t&8 != 0, t == 0:
		return 0 // text
	}
	return 0
}

// GetToc 抓取目录。返回值中的 declaredType 是书源在规则 JS 里声明的书籍类型
// （-1 表示未声明），书源用它在目录阶段把听书/漫画/短剧源标成对应类型。
func (s *ReaderService) GetToc(ctx context.Context, userID, sourceID, sourceURL, bookURL, tocURL string) ([]TocChapter, error) {
	src, bs, err := s.loadSourceFlexible(ctx, sourceID, sourceURL)
	if err != nil {
		return nil, err
	}
	chapters, declared, err := s.getTocFrom(ctx, src, bs, bookURL, tocURL)
	if err != nil {
		return nil, err
	}
	// 对应 legado：书源给 book.type 赋值后 legado 会持久化到 Book.type。
	// 书架的「开始阅读」与详情页都会在这里拉目录，此时书籍已在书架时即可写回。
	s.applyDeclaredBookType(ctx, userID, src.SourceURL, bookURL, declared)
	return chapters, nil
}

// applyDeclaredBookType 把书源声明的书籍类型写回书架记录。
func (s *ReaderService) applyDeclaredBookType(ctx context.Context, userID, origin, bookURL string, declared int) {
	if declared < 0 || userID == "" || origin == "" || bookURL == "" {
		return
	}
	book, err := s.repo.FindBookByURL(ctx, userID, origin, bookURL)
	if err != nil || book == nil || book.Type == declared {
		return
	}
	book.Type = declared
	if err := s.repo.UpdateBook(ctx, book); err != nil && s.log != nil {
		s.log.Warn("reader: 写回书籍类型失败", zap.String("book", book.ID), zap.Error(err))
	}
}

func (s *ReaderService) getTocFrom(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, bookURL, tocURL string) ([]TocChapter, int, error) {
	tr := bs.RuleToc
	if tr == nil || SPtr(tr.ChapterList) == "" {
		return nil, -1, fmt.Errorf("书源未配置目录规则")
	}
	if tocURL == "" {
		tocURL = bookURL
	}
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	runner := sess.runner("", 0)
	req, err := rule.ParseAnalyzeUrlWithJS(tocURL, "", 0, sess.srcURL(), runner)
	if err != nil {
		return nil, -1, err
	}
	if req.Unsupported != nil {
		return nil, -1, req.Unsupported
	}
	sess.applyHeaders(req)
	body, finalURL, _, err := sess.fetch(req)
	if err != nil {
		return nil, -1, err
	}
	ar := sess.newAnalyzer("", 0, body, finalURL)
	sess.applyBookContext(ar, bookURL, nil, "", 0)

	elements, err := ar.GetElements(SPtr(tr.ChapterList))
	if err != nil {
		return nil, -1, err
	}
	declaredType := -1
	if t, ok := ar.BookTypeOverride(); ok {
		declaredType = normalizeBookType(t)
	}
	var chapters []TocChapter
	for i, el := range elements {
		title, err := ar.GetString(SPtr(tr.ChapterName), el, false)
		if err != nil || title == "" {
			continue
		}
		url, err := ar.GetString(SPtr(tr.ChapterURL), el, true)
		if err != nil || url == "" {
			continue
		}
		isVolume := false
		if SPtr(tr.IsVolume) != "" {
			if v, err := ar.GetString(SPtr(tr.IsVolume), el, false); err == nil {
				isVolume = v != "" && v != "false" && v != "0"
			}
		}
		updateTime, _ := ar.GetString(SPtr(tr.UpdateTime), el, false)
		chapters = append(chapters, TocChapter{
			Index: i, Title: title, URL: url, IsVolume: isVolume, UpdateTime: updateTime,
		})
	}
	// 章节规则可能逐条执行，取最后一次声明（书源是在元素循环前设置的）。
	if t, ok := ar.BookTypeOverride(); ok {
		declaredType = normalizeBookType(t)
	}
	return chapters, declaredType, nil
}

// ChapterContent 章节内容（按类型返回文本/音频/图片）。
type ChapterContent struct {
	Type       string   `json:"type"` // text / audio / image
	Content    string   `json:"content,omitempty"`
	Tracks     []string `json:"tracks,omitempty"`      // 音频播放地址（已改写为服务端签名代理）
	Images     []string `json:"images,omitempty"`      // 漫画图片列表（已改写为服务端签名代理）
	ImageStyle string   `json:"image_style,omitempty"` // 对应 legado ruleContent.imageStyle
	// Transcoding 为真表示该音轨走了服务端转码（源格式浏览器解不了），
	// 首次播放需要等转码完成，之后命中缓存秒开。
	Transcoding bool `json:"transcoding,omitempty"`
	// declaredType 书源在规则 JS 里声明的书籍类型（-1 表示未声明），
	// 由 GetContentForBook 写回书架记录（对应 legado Book.type）。
	declaredType int
}

// ─── 媒体代理（音频流 / 漫画图片，带防盗链头与 HMAC 签名） ──────────────────

// ProxyURL 将书源返回的媒体地址改写为签名代理地址。
// 签名 = HMAC-SHA256(jwtSecret, bookID|url)，防止代理被滥用为开放中转。
func (s *ReaderService) ProxyURL(bookID, rawURL string) string {
	if rawURL == "" || strings.HasPrefix(rawURL, "/api/") {
		return rawURL
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.Secrets.JWTSecret))
	mac.Write([]byte(bookID + "|" + rawURL))
	sig := hex.EncodeToString(mac.Sum(nil))[:32]
	return "/api/reader/media?b=" + url.QueryEscape(bookID) +
		"&u=" + base64.RawURLEncoding.EncodeToString([]byte(rawURL)) + "&s=" + sig
}

// VerifyProxyURL 校验签名并还原媒体地址。
func (s *ReaderService) VerifyProxyURL(bookID, encoded, sig string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("媒体地址解码失败")
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.Secrets.JWTSecret))
	mac.Write([]byte(bookID + "|" + string(raw)))
	expect := hex.EncodeToString(mac.Sum(nil))[:32]
	if !hmac.Equal([]byte(expect), []byte(sig)) {
		return "", fmt.Errorf("媒体地址签名校验失败")
	}
	return string(raw), nil
}

// FetchMedia 服务端拉取媒体资源（携带书源级请求头与 Referer，支持 Range 透传）。
// 调用方负责关闭 resp.Body。
func (s *ReaderService) FetchMedia(ctx context.Context, book *model.ReaderBook, rawURL, rangeHeader string) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range helper.HTTPHeaderPresets() {
		httpReq.Header.Set(k, v)
	}
	// 媒体流同样交给 net/http 管压缩：否则压缩过的资源会以原始字节透传给
	// 播放器/图片标签，表现为「打不开」。Range 请求服务端通常不压缩，
	// 解压后 resp 会去掉 Content-Length/Content-Encoding，透传逻辑不受影响。
	helper.StripAcceptEncoding(httpReq.Header)
	// 书源级请求头
	if s.repo != nil {
		if found, findErr := s.repo.GetSourceByURL(ctx, book.Origin); findErr == nil && found != nil && found.Header != "" {
			var headers map[string]any
			if json.Unmarshal([]byte(found.Header), &headers) == nil {
				for k, v := range headers {
					httpReq.Header.Set(k, fmt.Sprintf("%v", v))
				}
			}
		}
	}
	// 登录态：登录类书源的漫画/音频资源同样需要 Cookie 与 loginHeader 才能取到。
	if s.repo != nil {
		state := s.newSourceState(ctx, book.Origin)
		for k, v := range state.LoginHeaderMap() {
			if !strings.EqualFold(k, "cookie") && httpReq.Header.Get(k) == "" {
				httpReq.Header.Set(k, v)
			}
		}
		if httpReq.Header.Get("Cookie") == "" {
			if ck := state.CookieForRequest(rawURL); ck != "" {
				httpReq.Header.Set("Cookie", ck)
			}
		}
	}
	if httpReq.Header.Get("Referer") == "" && book.Origin != "" {
		httpReq.Header.Set("Referer", strings.TrimSuffix(book.Origin, "/")+"/")
	}
	if rangeHeader != "" {
		httpReq.Header.Set("Range", rangeHeader)
	}
	return s.http.Do(httpReq)
}

// RewritePlaylist 重写 m3u8 播放列表：分片与密钥地址改写为签名代理地址。
func (s *ReaderService) RewritePlaylist(bookID, playlistURL, text string) string {
	base, err := url.Parse(playlistURL)
	if err != nil {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") {
			// EXT-X-KEY / EXT-X-MAP 的 URI="..." 属性
			if idx := strings.Index(t, `URI="`); idx >= 0 {
				rest := t[idx+len(`URI="`):]
				if end := strings.Index(rest, `"`); end >= 0 {
					inner := rest[:end]
					abs := GetAbsoluteURLOf(base, inner)
					lines[i] = t[:idx] + `URI="` + s.ProxyURL(bookID, abs) + `"` + rest[end+1:]
				}
			}
			continue
		}
		lines[i] = s.ProxyURL(bookID, GetAbsoluteURLOf(base, t))
	}
	return strings.Join(lines, "\n")
}

// GetAbsoluteURLOf 以解析后的 base URL 拼绝对地址（内部工具）。
func GetAbsoluteURLOf(base *url.URL, ref string) string {
	parsed, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ref
	}
	return base.ResolveReference(parsed).String()
}

// GetContent 抓取正文（含 nextContentUrl 翻页合并与净化替换）。
// 未指定书籍类型时按书源声明的类型判断（详情页的临时阅读路径）。
func (s *ReaderService) GetContent(ctx context.Context, sourceID, sourceURL, bookURL, chapterURL string) (*ChapterContent, error) {
	src, bs, err := s.loadSourceFlexible(ctx, sourceID, sourceURL)
	if err != nil {
		return nil, err
	}
	return s.getContentFrom(ctx, src, bs, bookURL, chapterURL, -1)
}

// getContentFrom 抓取正文。
//
// bookType 是书架记录里的书籍类型（0文本/1音频/2图片），-1 表示未知、
// 退回用书源的 bookSourceType。注意不能直接用 bookSourceType：文本型聚合源
// 也会提供听书/漫画内容，真正的类型由书源在目录规则里声明（见 normalizeBookType）。
func (s *ReaderService) getContentFrom(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, bookURL, chapterURL string, bookType int) (*ChapterContent, error) {
	cr := bs.RuleContent
	if cr == nil || SPtr(cr.Content) == "" {
		return nil, fmt.Errorf("书源未配置正文规则")
	}
	var parts []string
	url := chapterURL
	lastFinalURL := ""
	// 书源可以在规则 JS 里声明书籍类型（听书源会把 type 改成音频），
	// 用最后一次声明为准；未声明时沿用书架记录里的类型。
	declaredType := -1
	// 整章（含翻页）共用一个会话，翻页期间 Cookie/变量变更保持一致。
	sess := s.newSession(ctx, src, bs)
	defer sess.close()
	for i := 0; i < maxContentNextPage; i++ {
		runner := sess.runner("", 0)
		req, err := rule.ParseAnalyzeUrlWithJS(url, "", 0, sess.srcURL(), runner)
		if err != nil {
			return nil, err
		}
		if req.Unsupported != nil {
			return nil, req.Unsupported
		}
		sess.applyHeaders(req)
		body, finalURL, _, err := sess.fetch(req)
		if err != nil {
			return nil, err
		}
		lastFinalURL = finalURL
		ar := sess.newAnalyzer("", 0, body, finalURL)
		sess.applyBookContext(ar, bookURL, nil, "", 0)

		list, err := ar.GetStringList(SPtr(cr.Content), nil, false)
		if err != nil {
			return nil, err
		}
		if t, ok := ar.BookTypeOverride(); ok {
			declaredType = normalizeBookType(t)
		}
		parts = append(parts, strings.Join(list, "\n"))
		if SPtr(cr.NextContentURL) == "" {
			break
		}
		next, err := ar.GetString(SPtr(cr.NextContentURL), nil, true)
		if err != nil || next == "" || next == url || next == finalURL {
			break
		}
		url = next
	}
	content := strings.Join(parts, "\n")
	if rr := SPtr(cr.ReplaceRegex); rr != "" {
		content = rule.ApplyReplaceRegexString(content, rr)
	}
	out := &ChapterContent{Content: content, declaredType: declaredType}
	// 书源在规则 JS 里声明的类型优先（听书源会把书籍标成音频），
	// 其次用书架记录里的类型，最后才退回书源的 bookSourceType。
	effective := bookType
	if declaredType >= 0 {
		effective = declaredType
	}
	if effective < 0 {
		effective = src.Type
	}
	switch effective {
	case 1: // 音频：逐行地址，按最终页面 URL 绝对化
		out.Type = "audio"
		for _, line := range splitURLLines(content) {
			if abs := rule.GetAbsoluteURL(lastFinalURL, line); abs != "" {
				out.Tracks = append(out.Tracks, abs)
			}
		}
	case 2: // 漫画/图片
		out.Type = "image"
		for _, line := range splitURLLines(content) {
			if abs := rule.GetAbsoluteURL(lastFinalURL, line); abs != "" {
				out.Images = append(out.Images, abs)
			}
		}
		out.ImageStyle = SPtr(cr.ImageStyle)
	default:
		out.Type = "text"
	}
	return out, nil
}

func splitURLLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ─── 书架 ──────────────────────────────────────────────────────────────────

// AddBook 将搜索结果加入书架。
func (s *ReaderService) AddBook(ctx context.Context, userID string, origin SearchOrigin, name, author, coverURL string) (*model.ReaderBook, error) {
	if existing, err := s.repo.FindBookByURL(ctx, userID, origin.Origin, origin.BookURL); err == nil && existing != nil {
		return existing, nil
	}
	book := &model.ReaderBook{
		UserID:     userID,
		Origin:     origin.Origin,
		OriginName: origin.OriginName,
		BookURL:    origin.BookURL,
		Name:       name,
		Author:     author,
		CoverURL:   coverURL,
		Type:       origin.OriginType,
	}
	if err := s.repo.CreateBook(ctx, book); err != nil {
		return nil, err
	}
	return book, nil
}

// GetBook 按 ID 取书（媒体代理等使用）。
func (s *ReaderService) GetBook(ctx context.Context, id string) (*model.ReaderBook, error) {
	return s.repo.GetBook(ctx, id)
}

// ListBooks 书架列表。
func (s *ReaderService) ListBooks(ctx context.Context, userID string) ([]model.ReaderBook, error) {
	books, err := s.repo.ListBooks(ctx, userID)
	if err != nil {
		return nil, err
	}
	// 网络书籍的 total_chapter_num 不落库，用已缓存的目录条数补齐，供书架显示未读章数。
	// 统计失败不影响书架列表本身。
	ids := make([]string, 0, len(books))
	for i := range books {
		ids = append(ids, books[i].ID)
	}
	if counts, err := s.repo.CountChaptersByBook(ctx, ids); err == nil {
		for i := range books {
			if n := counts[books[i].ID]; n > books[i].TotalChapterNum {
				books[i].TotalChapterNum = n
			}
		}
	}
	for i := range books {
		books[i].IsLocal = books[i].LocalPath != ""
		// 早期导入的本地 EPUB 没存封面，这里按需自愈一次
		s.BackfillLocalCover(ctx, &books[i])
	}
	return books, nil
}

// RemoveBook 移出书架（本地书籍顺带删掉落盘的正文文件）。
func (s *ReaderService) RemoveBook(ctx context.Context, userID, id string) error {
	book, err := s.repo.GetBook(ctx, id)
	if err == nil && book.UserID == userID {
		s.DeleteLocalBookFile(book)
	}
	return s.repo.DeleteBook(ctx, userID, id)
}

// SaveProgress 保存阅读进度（对应 legado durChapter*）。
func (s *ReaderService) SaveProgress(ctx context.Context, userID, bookID string, chapterIndex, pos int, chapterTitle string) error {
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil {
		return err
	}
	if book.UserID != userID {
		return fmt.Errorf("无权操作他人书架")
	}
	book.DurChapterIndex = chapterIndex
	book.DurChapterPos = pos
	book.DurChapterTitle = chapterTitle
	book.DurChapterTime = time.Now().UnixMilli()
	return s.repo.UpdateBook(ctx, book)
}

// SaveAudioConfig 保存听书跳过片头/片尾设置（对应 legado Book.openCredits/closeCredits，单位秒）。
func (s *ReaderService) SaveAudioConfig(ctx context.Context, userID, bookID string, openCredits, closeCredits int) error {
	if openCredits < 0 || closeCredits < 0 {
		return fmt.Errorf("片头/片尾秒数不能为负")
	}
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil {
		return err
	}
	if book.UserID != userID {
		return fmt.Errorf("无权操作他人书架")
	}
	book.OpenCredits = openCredits
	book.CloseCredits = closeCredits
	return s.repo.UpdateBook(ctx, book)
}

// ListChapters 目录缓存读取。
func (s *ReaderService) ListChapters(ctx context.Context, bookID string) ([]model.ReaderChapter, error) {
	return s.repo.ListChapters(ctx, bookID)
}

// ChapterInput 章节保存输入。
type ChapterInput struct {
	Index    int    `json:"index"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	IsVolume bool   `json:"is_volume"`
}

// SaveChapters 覆盖保存章节缓存。
func (s *ReaderService) SaveChapters(ctx context.Context, bookID string, chapters []ChapterInput) error {
	models := make([]model.ReaderChapter, 0, len(chapters))
	for _, c := range chapters {
		models = append(models, model.ReaderChapter{
			BookID:   bookID,
			Index:    c.Index,
			URL:      c.URL,
			Title:    c.Title,
			IsVolume: c.IsVolume,
		})
	}
	return s.repo.ReplaceChapters(ctx, bookID, models)
}

// WarmUpBookChaptersAsync 异步补一次目录缓存，不阻塞加入书架接口。
// 加入书架时只写书籍记录、不抓目录，书架就没有总章数可算未读；这里后台抓一次目录落库。
func (s *ReaderService) WarmUpBookChaptersAsync(ctx context.Context, userID string, book *model.ReaderBook) {
	if book == nil {
		return
	}
	// 请求结束后 gin 的 ctx 会被取消，先摘掉取消信号再开 goroutine。
	bg := context.WithoutCancel(ctx)
	go func() {
		warmCtx, cancel := context.WithTimeout(bg, 60*time.Second)
		defer cancel()
		s.WarmUpBookChapters(warmCtx, userID, book)
	}()
}

// WarmUpBookChapters 抓取目录并写入章节缓存，供书架显示未读章数。
// 本地书籍、已有目录缓存、书源未配目录规则的情况都会直接跳过，失败只记日志。
func (s *ReaderService) WarmUpBookChapters(ctx context.Context, userID string, book *model.ReaderBook) {
	if book == nil || book.LocalPath != "" {
		return
	}
	if existing, err := s.repo.ListChapters(ctx, book.ID); err == nil && len(existing) > 0 {
		return
	}
	chapters, err := s.GetToc(ctx, userID, "", book.Origin, book.BookURL, book.TocURL)
	if err != nil {
		if s.log != nil {
			s.log.Debug("reader: 预热目录失败", zap.String("book", book.ID), zap.Error(err))
		}
		return
	}
	if len(chapters) == 0 {
		return
	}
	inputs := make([]ChapterInput, 0, len(chapters))
	for _, ch := range chapters {
		inputs = append(inputs, ChapterInput{Index: ch.Index, Title: ch.Title, URL: ch.URL, IsVolume: ch.IsVolume})
	}
	if err := s.SaveChapters(ctx, book.ID, inputs); err != nil && s.log != nil {
		s.log.Warn("reader: 预热目录写入失败", zap.String("book", book.ID), zap.Error(err))
	}
}

// ListReplaceRules 用户替换规则列表。
func (s *ReaderService) ListReplaceRules(ctx context.Context, userID string) ([]model.ReaderReplaceRule, error) {
	return s.repo.ListReplaceRules(ctx, userID)
}

// ReplaceRuleInput 替换规则输入。
type ReplaceRuleInput struct {
	Name               string `json:"name"`
	GroupName          string `json:"group"`
	Pattern            string `json:"pattern"`
	Replacement        string `json:"replacement"`
	Scope              string `json:"scope"`
	ScopeTitle         bool   `json:"scope_title"`
	ScopeContent       bool   `json:"scope_content"`
	ExcludeScope       string `json:"exclude_scope"`
	IsEnabled          bool   `json:"is_enabled"`
	IsRegex            bool   `json:"is_regex"`
	TimeoutMillisecond int64  `json:"timeout_millisecond"`
	Order              int    `json:"order"`
}

// CreateReplaceRule 新增替换规则。
func (s *ReaderService) CreateReplaceRule(ctx context.Context, userID string, in ReplaceRuleInput) (*model.ReaderReplaceRule, error) {
	if strings.TrimSpace(in.Pattern) == "" {
		return nil, fmt.Errorf("替换规则不能为空")
	}
	rule := &model.ReaderReplaceRule{
		UserID:             userID,
		Name:               in.Name,
		GroupName:          in.GroupName,
		Pattern:            in.Pattern,
		Replacement:        in.Replacement,
		Scope:              in.Scope,
		ScopeTitle:         in.ScopeTitle,
		ScopeContent:       in.ScopeContent,
		ExcludeScope:       in.ExcludeScope,
		IsEnabled:          in.IsEnabled,
		IsRegex:            in.IsRegex,
		TimeoutMillisecond: in.TimeoutMillisecond,
		Order:              in.Order,
	}
	if err := s.repo.CreateReplaceRule(ctx, rule); err != nil {
		return nil, err
	}
	return rule, nil
}

// UpdateReplaceRule 更新替换规则。
func (s *ReaderService) UpdateReplaceRule(ctx context.Context, userID, id string, in ReplaceRuleInput) error {
	existing, err := s.repo.ListReplaceRules(ctx, userID)
	if err != nil {
		return err
	}
	var target *model.ReaderReplaceRule
	for i := range existing {
		if existing[i].ID == id {
			target = &existing[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("规则不存在")
	}
	target.Name = in.Name
	target.GroupName = in.GroupName
	target.Pattern = in.Pattern
	target.Replacement = in.Replacement
	target.Scope = in.Scope
	target.ScopeTitle = in.ScopeTitle
	target.ScopeContent = in.ScopeContent
	target.ExcludeScope = in.ExcludeScope
	target.IsEnabled = in.IsEnabled
	target.IsRegex = in.IsRegex
	target.TimeoutMillisecond = in.TimeoutMillisecond
	target.Order = in.Order
	return s.repo.UpdateReplaceRule(ctx, target)
}

// DeleteReplaceRule 删除替换规则。
func (s *ReaderService) DeleteReplaceRule(ctx context.Context, userID, id string) error {
	return s.repo.DeleteReplaceRule(ctx, userID, id)
}

// ─── 书架维度正文（含用户替换净化） ─────────────────────────────────────────

// GetContentForBook 按书架书籍 + 章节序号取正文：
// 解析书源 → 章节缓存 → 抓正文 → 书源 replaceRegex → 用户替换净化规则。
func (s *ReaderService) GetContentForBook(ctx context.Context, userID, bookID string, chapterIndex int) (*ChapterContent, error) {
	book, err := s.repo.GetBook(ctx, bookID)
	if err != nil {
		return nil, err
	}
	// 本地导入书籍：正文直接从本地文件读，不走书源
	if book.LocalPath != "" {
		return s.LocalChapterContent(ctx, userID, bookID, chapterIndex)
	}
	chapters, err := s.repo.ListChapters(ctx, bookID)
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 {
		return nil, fmt.Errorf("章节缓存为空，请先在详情页刷新目录")
	}
	if chapterIndex < 0 || chapterIndex >= len(chapters) {
		return nil, fmt.Errorf("章节序号越界（共 %d 章）", len(chapters))
	}
	ch := chapters[chapterIndex]
	src, bs, err := s.loadSourceFlexible(ctx, "", book.Origin)
	if err != nil {
		return nil, err
	}
	out, err := s.getContentFrom(ctx, src, bs, book.BookURL, ch.URL, book.Type)
	if err != nil {
		return nil, err
	}
	// 书源在规则 JS 里声明的书籍类型写回书架记录：听书/漫画/短剧源靠它
	// 声明类型，否则下次阅读又会按导入时的默认类型（文本）渲染。
	if out.declaredType >= 0 && out.declaredType != book.Type {
		book.Type = out.declaredType
		if err := s.repo.UpdateBook(ctx, book); err != nil && s.log != nil {
			s.log.Warn("reader: 写回书籍类型失败", zap.String("book", book.ID), zap.Error(err))
		}
	}
	if out.Type == "text" {
		out.Content = s.applyUserReplaceRules(ctx, userID, book.Name, out.Content)
	}
	// 音频/图片地址改写为签名代理（浏览器播放/展示无法带防盗链头）
	for i, t := range out.Tracks {
		out.Tracks[i] = s.ProxyURL(book.ID, t)
	}
	for i, img := range out.Images {
		out.Images[i] = s.ProxyURL(book.ID, img)
	}
	return out, nil
}

// applyUserReplaceRules 应用启用的用户替换规则（对应 legado ReplaceRule 作用链）。
func (s *ReaderService) applyUserReplaceRules(ctx context.Context, userID, bookName, content string) string {
	if content == "" {
		return content
	}
	rules, err := s.repo.ListReplaceRules(ctx, userID)
	if err != nil {
		return content
	}
	for _, r := range rules {
		if !r.IsEnabled || r.Pattern == "" || !r.ScopeContent {
			continue
		}
		// 作用范围 / 排除范围按书名匹配（对应 legado scope / excludeScope）
		if r.Scope != "" && !strings.Contains(bookName, r.Scope) {
			continue
		}
		if r.ExcludeScope != "" && strings.Contains(bookName, r.ExcludeScope) {
			continue
		}
		content = rule.ApplyUserReplace(content, r.Pattern, r.Replacement, r.IsRegex, r.TimeoutMillisecond)
	}
	return content
}

// ─── 书源调试（对应 BookSourceDebugModel 全链路） ───────────────────────────

// Debug 全链路调试：搜索 → 详情 → 目录 → 正文，返回逐条日志。
// SmokeLog 冒烟/调试日志行。
type SmokeLog struct {
	Stage   string `json:"stage"`
	Level   string `json:"level"` // info / error
	Message string `json:"message"`
}

// SmokeChainResult 单书源全链路冒烟结果。
type SmokeChainResult struct {
	SourceID   string     `json:"source_id"`
	SourceName string     `json:"source_name"`
	SourceURL  string     `json:"source_url"`
	Type       int        `json:"type"`
	OK         bool       `json:"ok"`
	FailedAt   string     `json:"failed_at,omitempty"` // search / info / toc / content
	Error      string     `json:"error,omitempty"`
	SearchHits int        `json:"search_hits"`
	Chapters   int        `json:"chapters"`
	ContentLen int        `json:"content_len"`
	ElapsedMS  int64      `json:"elapsed_ms"`
	Logs       []SmokeLog `json:"logs"`
}

// SmokeChain 对单个书源跑 搜索→详情→目录→正文 全链路，
// 返回结构化结果（书源调试接口与冒烟 CLI 共用）。
func (s *ReaderService) SmokeChain(ctx context.Context, sourceID string, src *model.ReaderBookSource, bs *BookSource, key string) *SmokeChainResult {
	res := &SmokeChainResult{Logs: []SmokeLog{}}
	if src != nil {
		res.SourceID = src.ID
		res.SourceName = src.Name
		res.SourceURL = src.SourceURL
		res.Type = src.Type
	} else if bs != nil {
		res.SourceName = bs.BookSourceName
		res.SourceURL = bs.BookSourceURL
		res.Type = bs.Type()
	}
	start := time.Now()
	logf := func(stage, level, format string, args ...any) {
		res.Logs = append(res.Logs, SmokeLog{Stage: stage, Level: level, Message: fmt.Sprintf(format, args...)})
	}
	fail := func(stage string, err error) *SmokeChainResult {
		res.FailedAt = stage
		res.Error = err.Error()
		res.ElapsedMS = time.Since(start).Milliseconds()
		logf(stage, "error", "%s 失败: %v", stage, err)
		return res
	}

	logf("search", "info", "搜索关键词: %s", key)
	books, err := s.searchInSource(ctx, src, bs, key, 1)
	if err != nil {
		return fail("search", err)
	}
	res.SearchHits = len(books)
	if len(books) == 0 {
		return fail("search", fmt.Errorf("搜索结果为空"))
	}
	logf("search", "info", "搜索到 %d 条结果", len(books))
	for i, b := range books {
		if i >= 3 {
			break
		}
		logf("search", "info", "结果[%d] %s / %s", i, b.Name, b.Author)
	}
	first := books[0]

	logf("info", "info", "访问详情页: %s", first.BookURL)
	info, err := s.getBookInfoFrom(ctx, src, bs, first.BookURL)
	if err != nil {
		return fail("info", err)
	}
	logf("info", "info", "书名: %s 作者: %s 最新章节: %s", info.Name, info.Author, info.LatestChapter)

	logf("toc", "info", "访问目录页: %s", info.TocURL)
	chapters, _, err := s.getTocFrom(ctx, src, bs, first.BookURL, info.TocURL)
	if err != nil {
		return fail("toc", err)
	}
	res.Chapters = len(chapters)
	logf("toc", "info", "共 %d 章", len(chapters))
	for i, c := range chapters {
		if i >= 3 {
			break
		}
		logf("toc", "info", "章节[%d] %s", c.Index, c.Title)
	}

	for _, c := range chapters {
		if c.IsVolume || c.URL == "" {
			continue
		}
		logf("content", "info", "访问正文: %s", c.URL)
		content, err := s.getContentFrom(ctx, src, bs, first.BookURL, c.URL, -1)
		if err != nil {
			return fail("content", err)
		}
		res.ContentLen = len([]rune(content.Content))
		text := content.Content
		if len([]rune(text)) > 200 {
			text = string([]rune(text)[:200]) + "..."
		}
		logf("content", "info", "正文预览(%d字): %s", res.ContentLen, text)
		break
	}
	if res.ContentLen == 0 {
		return fail("content", fmt.Errorf("未取到正文（可能全是卷名）"))
	}
	res.OK = true
	res.ElapsedMS = time.Since(start).Milliseconds()
	logf("done", "info", "链路完成，耗时 %dms", res.ElapsedMS)
	return res
}

// SmokeSource 直接对一段书源 JSON 跑全链路（冒烟 CLI 用，不落库）。
func (s *ReaderService) SmokeSource(ctx context.Context, raw string, key string) *SmokeChainResult {
	bs, err := ParseBookSource(raw)
	var res *SmokeChainResult
	if err != nil {
		res = &SmokeChainResult{OK: false, FailedAt: "parse", Error: err.Error(), Logs: []SmokeLog{}}
		return res
	}
	src := &model.ReaderBookSource{
		Name:        bs.BookSourceName,
		GroupName:   strings.TrimSpace(SPtr(bs.BookSourceGroup)),
		Type:        bs.Type(),
		SourceURL:   bs.BookSourceURL,
		RawJSON:     raw,
		Header:      SPtr(bs.Header),
		Enabled:     true,
		CustomOrder: IPtr(bs.CustomOrder),
	}
	return s.SmokeChain(ctx, "", src, bs, key)
}

// Debug 书源调试接口：返回逐条日志字符串（前端展示用）。
func (s *ReaderService) Debug(ctx context.Context, sourceID, key string) ([]string, error) {
	src, bs, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	res := s.SmokeChain(ctx, src.ID, src, bs, key)
	out := make([]string, 0, len(res.Logs))
	for _, l := range res.Logs {
		prefix := "[info]"
		if l.Level == "error" {
			prefix = "[错误]"
		}
		out = append(out, fmt.Sprintf("%s %s", prefix, l.Message))
	}
	return out, nil
}

// loadSource 加载书源记录与解析结构。
func (s *ReaderService) loadSource(ctx context.Context, sourceID string) (*model.ReaderBookSource, *BookSource, error) {
	src, err := s.repo.GetSource(ctx, sourceID)
	if err != nil {
		return nil, nil, err
	}
	bs, err := ParseBookSource(src.RawJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("书源 JSON 解析失败: %w", err)
	}
	return src, bs, nil
}

// loadSourceFlexible 按 ID 或 URL 加载书源（书架上只存 origin URL，
// 前端阅读链路用 source_url 定位书源）。
func (s *ReaderService) loadSourceFlexible(ctx context.Context, sourceID, sourceURL string) (*model.ReaderBookSource, *BookSource, error) {
	if sourceID != "" {
		return s.loadSource(ctx, sourceID)
	}
	if sourceURL == "" {
		return nil, nil, fmt.Errorf("缺少书源标识（source_id 或 source_url）")
	}
	src, err := s.repo.GetSourceByURL(ctx, sourceURL)
	if err != nil {
		return nil, nil, fmt.Errorf("书源不存在或已被删除")
	}
	bs, err := ParseBookSource(src.RawJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("书源 JSON 解析失败: %w", err)
	}
	return src, bs, nil
}

func applySourceHeaders(req *rule.Request, src *model.ReaderBookSource) {
	if src.Header == "" {
		return
	}
	var headers map[string]any
	if json.Unmarshal([]byte(src.Header), &headers) == nil {
		for k, v := range headers {
			if _, ok := req.Headers[k]; !ok {
				req.Headers[k] = fmt.Sprintf("%v", v)
			}
		}
	}
}
