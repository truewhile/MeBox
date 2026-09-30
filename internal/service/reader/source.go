// Package reader — legado 书源兼容的阅读子系统服务层。
// 本文件对应 legado data/entities/BookSource.kt 的 JSON 结构。
package reader

import (
	"encoding/json"
	"strings"
)

// BookSource 书源 JSON 结构（字段与 legado 实体一致，未知字段忽略）。
type BookSource struct {
	RawJSON           string         `json:"-"`
	BookSourceURL     string         `json:"bookSourceUrl"`
	BookSourceName    string         `json:"bookSourceName"`
	BookSourceGroup   *string        `json:"bookSourceGroup"`
	BookSourceType    *int           `json:"bookSourceType"` // 0文本 1音频 2图片 3文件 4视频
	BookURLPattern    *string        `json:"bookUrlPattern"`
	CustomOrder       *int           `json:"customOrder"`
	Enabled           *bool          `json:"enabled"`
	EnabledExplore    *bool          `json:"enabledExplore"`
	EnabledCookieJar  *bool          `json:"enabledCookieJar"`
	ConcurrentRate    *string        `json:"concurrentRate"`
	Header            *string        `json:"header"`
	LoginURL          *string        `json:"loginUrl"`
	BookSourceComment *string        `json:"bookSourceComment"`
	LastUpdateTime    *int64         `json:"lastUpdateTime"`
	RespondTime       *int64         `json:"respondTime"`
	Weight            *int           `json:"weight"`
	ExploreURL        *string        `json:"exploreUrl"`
	SearchURL         *string        `json:"searchUrl"`
	RuleExplore       *ExploreRule   `json:"ruleExplore"`
	RuleSearch        *SearchRule    `json:"ruleSearch"`
	RuleBookInfo      *BookInfoRule  `json:"ruleBookInfo"`
	RuleToc           *TocRule       `json:"ruleToc"`
	RuleContent       *ContentRule   `json:"ruleContent"`
	VariableComment   *string        `json:"variableComment"`
	Variables         map[string]any `json:"-"`
	RawVariables      *string        `json:"variables"`
}

// SearchRule 搜索规则。
type SearchRule struct {
	CheckKeyWord *string `json:"checkKeyWord"`
	BookList     *string `json:"bookList"`
	Name         *string `json:"name"`
	Author       *string `json:"author"`
	Kind         *string `json:"kind"`
	WordCount    *string `json:"wordCount"`
	LastChapter  *string `json:"lastChapter"`
	Intro        *string `json:"intro"`
	CoverURL     *string `json:"coverUrl"`
	BookURL      *string `json:"bookUrl"`
}

// BookInfoRule 详情规则。
type BookInfoRule struct {
	Init         *string `json:"init"`
	Name         *string `json:"name"`
	Author       *string `json:"author"`
	Kind         *string `json:"kind"`
	WordCount    *string `json:"wordCount"`
	LastChapter  *string `json:"lastChapter"`
	Intro        *string `json:"intro"`
	CoverURL     *string `json:"coverUrl"`
	TocURL       *string `json:"tocUrl"`
	CanReName    *string `json:"canReName"`
	DownloadUrls *string `json:"downloadUrls"`
}

// TocRule 目录规则。
type TocRule struct {
	PreUpdateJs *string `json:"preUpdateJs"`
	ChapterList *string `json:"chapterList"`
	ChapterName *string `json:"chapterName"`
	ChapterURL  *string `json:"chapterUrl"`
	IsVolume    *string `json:"isVolume"`
	UpdateTime  *string `json:"updateTime"`
}

// ContentRule 正文规则。
type ContentRule struct {
	Content        *string `json:"content"`
	NextContentURL *string `json:"nextContentUrl"`
	WebJs          *string `json:"webJs"`
	SourceRegex    *string `json:"sourceRegex"`
	ReplaceRegex   *string `json:"replaceRegex"`
	ImageStyle     *string `json:"imageStyle"`
	PayAction      *string `json:"payAction"`
}

// ExploreRule 发现规则。
type ExploreRule struct {
	BookList      *string `json:"bookList"`
	Name          *string `json:"name"`
	Author        *string `json:"author"`
	Kind          *string `json:"kind"`
	WordCount     *string `json:"wordCount"`
	LastChapter   *string `json:"lastChapter"`
	Intro         *string `json:"intro"`
	CoverURL      *string `json:"coverUrl"`
	BookURL       *string `json:"bookUrl"`
	ExploreURL    *string `json:"exploreUrl"`
	ExploreKinds  *string `json:"exploreKinds"`
	CheckKeyWord  *string `json:"checkKeyWord"`
}

// ParseBookSource 将书源 JSON 解析为结构体。
func ParseBookSource(raw string) (*BookSource, error) {
	var bs BookSource
	if err := json.Unmarshal([]byte(raw), &bs); err != nil {
		return nil, err
	}
	bs.RawJSON = raw
	if bs.RawVariables != nil && strings.TrimSpace(*bs.RawVariables) != "" {
		_ = json.Unmarshal([]byte(*bs.RawVariables), &bs.Variables)
	}
	return &bs, nil
}

// SourceProps 书源 JSON 原样转为 map（注入 JS 的 `source` 对象）。
func (b *BookSource) SourceProps() map[string]any {
	if b.RawJSON == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(b.RawJSON), &m) != nil {
		return nil
	}
	return m
}

// Type 返回书源类型（默认文本）。
func (b *BookSource) Type() int {
	if b.BookSourceType == nil {
		return 0
	}
	return *b.BookSourceType
}

// SPtr 取字符串指针字段值。
func SPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// IPtr 取整型指针字段值。
func IPtr(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}
