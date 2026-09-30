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
	cfg  *config.Config
	log  *zap.Logger
	repo *repository.ReaderRepository
	http *http.Client
}

// NewReaderService 创建服务。
func NewReaderService(cfg *config.Config, log *zap.Logger, repos *repository.Container) *ReaderService {
	return &ReaderService{
		cfg:  cfg,
		log:  log,
		repo: repos.Reader,
		http: helper.NewSiteHTTPClient(30, true),
	}
}

// ─── 书源导入与管理 ─────────────────────────────────────────────────────────

// ImportSources 导入书源：支持 JSON 数组 / 单对象 / Base64 / 网络URL。
// 返回导入数量。
func (s *ReaderService) ImportSources(ctx context.Context, text string) (int, error) {
	text = strings.TrimSpace(text)
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
	text = strings.TrimSpace(text)
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

// ListSources 书源列表。
func (s *ReaderService) ListSources(ctx context.Context) ([]model.ReaderBookSource, error) {
	return s.repo.ListSources(ctx)
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
	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}
	target := req.URLNoQuery
	if target == "" {
		target = req.URL
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
	if len(cookieStrs) > 0 {
		rule.CookieJarRecord(finalURL, cookieStrs)
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

// newRuleAnalyzer 为指定书源构建规则解析器（注入书源变量与 JS 运行时）。
func (s *ReaderService) newRuleAnalyzer(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, key string, page int, body, finalURL string) *rule.AnalyzeRule {
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	applySourceVariables(ar, bs)
	ar.SetJSRunner(s.jsRunnerFor(ctx, src, bs, key, page).ForAnalyzer(ar))
	return ar
}

// jsRunnerFor 为本次请求构建 JS 运行时（网络桥回 execute，携带书源上下文）。
func (s *ReaderService) jsRunnerFor(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, key string, page int) *rule.JSRunner {
	return rule.NewJSRunner(rule.JSConfig{
		Fetch: func(req *rule.Request) (string, string, int, error) {
			return s.execute(ctx, req)
		},
		SourceProps: bs.SourceProps(),
		Log: func(msg string) {
			if s.log != nil {
				s.log.Info("reader:source-js",
					zap.String("source", srcNameOf(src, bs)), zap.String("log", msg))
			}
		},
		BaseURL: src.SourceURL,
		Key:     key,
		Page:    page,
	})
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
		mu      sync.Mutex
		hits    []searchHit
		skipped []SearchSkipped
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
	runner := s.jsRunnerFor(ctx, src, bs, key, page)
	req, err := rule.ParseAnalyzeUrlWithJS(searchURL, key, page, src.SourceURL, runner)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	// 书源级请求头
	if src.Header != "" {
		var headers map[string]any
		if json.Unmarshal([]byte(src.Header), &headers) == nil {
			for k, v := range headers {
				if _, ok := req.Headers[k]; !ok {
					req.Headers[k] = fmt.Sprintf("%v", v)
				}
			}
		}
	}
	body, finalURL, _, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := s.newRuleAnalyzer(ctx, src, bs, key, page, body, finalURL)

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
	runner := s.jsRunnerFor(ctx, src, bs, "", 0)
	req, err := rule.ParseAnalyzeUrlWithJS(bookURL, "", 0, src.SourceURL, runner)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	applySourceHeaders(req, src)
	body, finalURL, _, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := s.newRuleAnalyzer(ctx, src, bs, "", 0, body, finalURL)

	info := &BookInfo{BookURL: bookURL, TocURL: bookURL}
	if initRule := SPtr(bir.Init); initRule != "" {
		// init 规则返回 JSON 对象时合并字段（对应 legado ruleBookInfo.init）
		if initVal, err := ar.GetString(initRule, nil, false); err == nil && initVal != "" {
			if strings.HasPrefix(strings.TrimSpace(initVal), "{") {
				var m map[string]string
				if json.Unmarshal([]byte(initVal), &m) == nil {
					if v, ok := m["name"]; ok {
						info.Name = v
					}
					if v, ok := m["author"]; ok {
						info.Author = v
					}
					if v, ok := m["intro"]; ok {
						info.Intro = v
					}
					if v, ok := m["tocUrl"]; ok {
						info.TocURL = v
					}
				}
			}
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

// GetToc 抓取目录。
func (s *ReaderService) GetToc(ctx context.Context, sourceID, sourceURL, bookURL, tocURL string) ([]TocChapter, error) {
	src, bs, err := s.loadSourceFlexible(ctx, sourceID, sourceURL)
	if err != nil {
		return nil, err
	}
	return s.getTocFrom(ctx, src, bs, bookURL, tocURL)
}

func (s *ReaderService) getTocFrom(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, bookURL, tocURL string) ([]TocChapter, error) {
	tr := bs.RuleToc
	if tr == nil || SPtr(tr.ChapterList) == "" {
		return nil, fmt.Errorf("书源未配置目录规则")
	}
	if tocURL == "" {
		tocURL = bookURL
	}
	runner := s.jsRunnerFor(ctx, src, bs, "", 0)
	req, err := rule.ParseAnalyzeUrlWithJS(tocURL, "", 0, src.SourceURL, runner)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	applySourceHeaders(req, src)
	body, finalURL, _, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := s.newRuleAnalyzer(ctx, src, bs, "", 0, body, finalURL)

	elements, err := ar.GetElements(SPtr(tr.ChapterList))
	if err != nil {
		return nil, err
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
	return chapters, nil
}

// ChapterContent 章节内容（按类型返回文本/音频/图片）。
type ChapterContent struct {
	Type       string   `json:"type"` // text / audio / image
	Content    string   `json:"content,omitempty"`
	Tracks     []string `json:"tracks,omitempty"`      // 音频播放地址（已改写为服务端签名代理）
	Images     []string `json:"images,omitempty"`      // 漫画图片列表（已改写为服务端签名代理）
	ImageStyle string   `json:"image_style,omitempty"` // 对应 legado ruleContent.imageStyle
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
func (s *ReaderService) GetContent(ctx context.Context, sourceID, sourceURL, bookURL, chapterURL string) (*ChapterContent, error) {
	src, bs, err := s.loadSourceFlexible(ctx, sourceID, sourceURL)
	if err != nil {
		return nil, err
	}
	return s.getContentFrom(ctx, src, bs, bookURL, chapterURL)
}

func (s *ReaderService) getContentFrom(ctx context.Context, src *model.ReaderBookSource, bs *BookSource, bookURL, chapterURL string) (*ChapterContent, error) {
	cr := bs.RuleContent
	if cr == nil || SPtr(cr.Content) == "" {
		return nil, fmt.Errorf("书源未配置正文规则")
	}
	var parts []string
	url := chapterURL
	lastFinalURL := ""
	for i := 0; i < maxContentNextPage; i++ {
		runner := s.jsRunnerFor(ctx, src, bs, "", 0)
		req, err := rule.ParseAnalyzeUrlWithJS(url, "", 0, src.SourceURL, runner)
		if err != nil {
			return nil, err
		}
		if req.Unsupported != nil {
			return nil, req.Unsupported
		}
		applySourceHeaders(req, src)
		body, finalURL, _, err := s.execute(ctx, req)
		if err != nil {
			return nil, err
		}
		lastFinalURL = finalURL
		ar := s.newRuleAnalyzer(ctx, src, bs, "", 0, body, finalURL)

		list, err := ar.GetStringList(SPtr(cr.Content), nil, false)
		if err != nil {
			return nil, err
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
	out := &ChapterContent{Content: content}
	switch src.Type {
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
	return s.repo.ListBooks(ctx, userID)
}

// RemoveBook 移出书架。
func (s *ReaderService) RemoveBook(ctx context.Context, userID, id string) error {
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
	out, err := s.GetContent(ctx, "", book.Origin, book.BookURL, ch.URL)
	if err != nil {
		return nil, err
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
	chapters, err := s.getTocFrom(ctx, src, bs, first.BookURL, info.TocURL)
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
		content, err := s.getContentFrom(ctx, src, bs, first.BookURL, c.URL)
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
