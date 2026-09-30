// Package reader — 阅读子系统服务：书源导入管理、搜索、详情、目录、正文。
// 业务语义对齐 legado 的 WebBook / SearchModel / BookSourceDebugModel。
package reader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		body, _, err := s.execute(ctx, &rule.Request{Method: "GET", URL: text, URLNoQuery: text, Headers: map[string]string{}})
		if err != nil {
			return 0, fmt.Errorf("拉取书源失败: %w", err)
		}
		text = body
	}
	sources := parseSourcePayload(text)
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

// parseSourcePayload 识别 JSON 数组 / 单对象 / Base64 / 每行一个对象。
func parseSourcePayload(text string) []string {
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

// execute 执行 rule.Request，返回（解码后 body, 最终 URL）。
func (s *ReaderService) execute(ctx context.Context, req *rule.Request) (string, string, error) {
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
		return "", "", err
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
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", "", err
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
	if strings.EqualFold(charsetFromContentType(resp.Header.Get("Content-Type")), "xml") &&
		!strings.HasPrefix(strings.TrimSpace(body), "<?xml") {
		body = "<?xml version=\"1.0\"?>" + body
	}
	return body, finalURL, nil
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

// newRuleAnalyzer 为指定书源构建规则解析器（注入书源变量）。
func (s *ReaderService) newRuleAnalyzer(bs *BookSource, body, finalURL string) *rule.AnalyzeRule {
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	applySourceVariables(ar, bs)
	return ar
}

// fetchViaRule 解析 URL 规则并抓取，返回 (body, 最终URL)。
func (s *ReaderService) fetchViaRule(ctx context.Context, urlRule, key string, page int, baseUrl string) (*rule.AnalyzeRule, error) {
	req, err := rule.ParseAnalyzeUrl(urlRule, key, page, baseUrl)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	body, finalURL, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	return ar, nil
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
			books, err := s.searchInSource(gctxSrc, &src, key, 1)
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
func (s *ReaderService) searchInSource(ctx context.Context, src *model.ReaderBookSource, key string, page int) ([]SearchBook, error) {
	bs, err := ParseBookSource(src.RawJSON)
	if err != nil {
		return nil, fmt.Errorf("书源 JSON 解析失败")
	}
	searchURL := SPtr(bs.SearchURL)
	if searchURL == "" {
		return nil, fmt.Errorf("书源未配置搜索地址")
	}
	sr := bs.RuleSearch
	if sr == nil || SPtr(sr.BookList) == "" {
		return nil, fmt.Errorf("书源未配置搜索列表规则")
	}
	req, err := rule.ParseAnalyzeUrl(searchURL, key, page, src.SourceURL)
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
	body, finalURL, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	applySourceVariables(ar, bs)

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
	bir := bs.RuleBookInfo
	if bir == nil {
		return nil, fmt.Errorf("书源未配置详情规则")
	}
	req, err := rule.ParseAnalyzeUrl(bookURL, "", 0, src.SourceURL)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	applySourceHeaders(req, src)
	body, finalURL, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	applySourceVariables(ar, bs)

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
	tr := bs.RuleToc
	if tr == nil || SPtr(tr.ChapterList) == "" {
		return nil, fmt.Errorf("书源未配置目录规则")
	}
	if tocURL == "" {
		tocURL = bookURL
	}
	req, err := rule.ParseAnalyzeUrl(tocURL, "", 0, src.SourceURL)
	if err != nil {
		return nil, err
	}
	if req.Unsupported != nil {
		return nil, req.Unsupported
	}
	applySourceHeaders(req, src)
	body, finalURL, err := s.execute(ctx, req)
	if err != nil {
		return nil, err
	}
	ar := rule.NewAnalyzeRule()
	ar.SetContent(body, finalURL)
	applySourceVariables(ar, bs)

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
	Type    string   `json:"type"` // text / audio / image
	Content string   `json:"content,omitempty"`
	Tracks  []string `json:"tracks,omitempty"` // 音频播放地址（m3u8/直链）
	Images  []string `json:"images,omitempty"` // 漫画图片列表
}

// GetContent 抓取正文（含 nextContentUrl 翻页合并与净化替换）。
func (s *ReaderService) GetContent(ctx context.Context, sourceID, sourceURL, bookURL, chapterURL string) (*ChapterContent, error) {
	src, bs, err := s.loadSourceFlexible(ctx, sourceID, sourceURL)
	if err != nil {
		return nil, err
	}
	cr := bs.RuleContent
	if cr == nil || SPtr(cr.Content) == "" {
		return nil, fmt.Errorf("书源未配置正文规则")
	}
	var parts []string
	url := chapterURL
	for i := 0; i < maxContentNextPage; i++ {
		req, err := rule.ParseAnalyzeUrl(url, "", 0, src.SourceURL)
		if err != nil {
			return nil, err
		}
		if req.Unsupported != nil {
			return nil, req.Unsupported
		}
		applySourceHeaders(req, src)
		body, finalURL, err := s.execute(ctx, req)
		if err != nil {
			return nil, err
		}
		ar := rule.NewAnalyzeRule()
		ar.SetContent(body, finalURL)
		applySourceVariables(ar, bs)

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
	case 1:
		out.Type = "audio"
		out.Tracks = splitURLLines(content)
	case 2:
		out.Type = "image"
		out.Images = splitURLLines(content)
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

// ─── 书源调试（对应 BookSourceDebugModel 全链路） ───────────────────────────

// Debug 全链路调试：搜索 → 详情 → 目录 → 正文，返回逐条日志。
func (s *ReaderService) Debug(ctx context.Context, sourceID, key string) ([]string, error) {
	src, _, err := s.loadSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	logf("搜索关键词: %s", key)
	books, err := s.searchInSource(ctx, src, key, 1)
	if err != nil {
		logf("搜索失败: %v", err)
		return logs, nil
	}
	if len(books) == 0 {
		logf("搜索结果为空")
		return logs, nil
	}
	logf("搜索到 %d 条结果", len(books))
	for i, b := range books {
		if i >= 3 {
			break
		}
		logf("结果[%d] %s / %s", i, b.Name, b.Author)
	}
	first := books[0]
	logf("访问详情页: %s", first.BookURL)
	info, err := s.GetBookInfo(ctx, sourceID, "", first.BookURL)
	if err != nil {
		logf("详情失败: %v", err)
		return logs, nil
	}
	logf("书名: %s 作者: %s 最新章节: %s", info.Name, info.Author, info.LatestChapter)
	logf("访问目录页: %s", info.TocURL)
	chapters, err := s.GetToc(ctx, sourceID, "", first.BookURL, info.TocURL)
	if err != nil {
		logf("目录失败: %v", err)
		return logs, nil
	}
	logf("共 %d 章", len(chapters))
	for i, c := range chapters {
		if i >= 3 {
			break
		}
		logf("章节[%d] %s", c.Index, c.Title)
	}
	// 找第一个非卷章节读正文
	for _, c := range chapters {
		if c.IsVolume {
			continue
		}
		logf("访问正文: %s", c.URL)
		content, err := s.GetContent(ctx, sourceID, "", first.BookURL, c.URL)
		if err != nil {
			logf("正文失败: %v", err)
			return logs, nil
		}
		text := content.Content
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		logf("正文预览: %s", text)
		break
	}
	logf("调试完成")
	return logs, nil
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
