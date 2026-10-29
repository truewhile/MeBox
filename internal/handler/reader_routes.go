// Package handler — 阅读（legado 书源兼容）子系统路由。
package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
	readerrule "github.com/truewhile/MeBox/internal/service/reader/rule"
)

func registerReaderRoutes(authed *gin.RouterGroup, svc *service.Container) {
	g := authed.Group("/reader")

	// 书源管理
	g.GET("/sources", readerListSourcesHandler(svc))
	g.POST("/sources/import", readerImportSourcesHandler(svc))
	g.PATCH("/sources/:id", readerUpdateSourceHandler(svc))
	g.DELETE("/sources/:id", readerDeleteSourceHandler(svc))
	g.POST("/sources/:id/debug", readerDebugSourceHandler(svc))

	// 书源登录与源变量（登录类书源必需）
	g.GET("/sources/:id/login", readerSourceLoginInfoHandler(svc))
	g.POST("/sources/:id/login", readerSourceLoginActionHandler(svc))
	g.DELETE("/sources/:id/login", readerSourceLogoutHandler(svc))
	g.PUT("/sources/:id/variable", readerSetSourceVariableHandler(svc))
	g.PUT("/sources/:id/login-info", readerSetSourceLoginInfoHandler(svc))

	// 书源 JS 的宿主浏览器（java.startBrowser / startBrowserAwait）：
	// 前端轮询待办 → 在 iframe 里承载页面 → 用户点 √ 回传 DOM。
	g.GET("/browser/pending", readerBrowserPendingHandler(svc))
	g.POST("/browser/result", readerBrowserResultHandler(svc))
	// 页面内的 fetch/XHR 经此转发（iframe 是不透明源，请求带不上书源 Cookie）
	g.POST("/browser/xhr", readerBrowserXHRHandler(svc))

	// 段评：用宿主浏览器打开评论地址（带书源 Cookie/登录态，对应书源的 showCmt）
	g.POST("/comments/open", readerOpenCommentHandler(svc))

	// 搜索（多源聚合）
	g.POST("/search", readerSearchHandler(svc))

	// 详情 / 目录 / 正文
	g.GET("/book-info", readerBookInfoHandler(svc))
	g.GET("/toc", readerTocHandler(svc))
	g.GET("/content", readerContentHandler(svc))

	// 书架
	g.GET("/books", readerListBooksHandler(svc))
	g.POST("/books", readerAddBookHandler(svc))
	// 本地书籍（TXT / EPUB 上传导入）
	g.POST("/local/books", readerImportLocalBookHandler(svc))
	// 服务器已有文件/目录导入：原地引用不复制，仅管理员（会读取允许根目录内的文件）
	g.POST("/local/books/from-path", middleware.AdminRequired(), readerImportLocalBookFromPathHandler(svc))
	g.POST("/local/audiobooks", middleware.AdminRequired(), readerImportLocalAudioDirHandler(svc))
	g.DELETE("/books/:id", readerRemoveBookHandler(svc))
	g.PUT("/books/:id/progress", readerSaveProgressHandler(svc))
	// 更新目录：重抓书架里全部网络书籍的目录，刷新章节缓存与「最近更新」时间
	g.POST("/shelf/refresh-toc", readerRefreshBooksTocHandler(svc))

	// 阅读器偏好（每个用户一条）：主题 / 排版 / 听书 / 书架展示设置，跨设备同步
	g.GET("/profile", readerGetProfileHandler(svc))
	g.PUT("/profile", readerSaveProfileHandler(svc))

	// 书架分组（每个用户一份）：组名 → 书籍 ID，仿影视模块的媒体库标签
	g.GET("/book-groups", readerGetBookGroupsHandler(svc))
	g.PUT("/book-groups", readerSetBookGroupsHandler(svc))
	// 换源：把书架里的书切到另一个书源（保留阅读进度，目录缓存按新源重建）
	g.POST("/books/:id/origin", readerSwitchOriginHandler(svc))
	g.PUT("/books/:id/audio-config", readerSaveAudioConfigHandler(svc))
	g.GET("/books/:id/chapters", readerListChaptersHandler(svc))
	g.POST("/books/:id/chapters", readerReplaceChaptersHandler(svc))
	g.GET("/books/:id/content", readerBookContentHandler(svc))
	// 批量取正文（离线缓存协议，对应 legado CacheBook）：返回每章内容与命中统计
	g.POST("/books/:id/content-batch", readerBookContentBatchHandler(svc))

	// 替换净化规则
	g.GET("/replace-rules", readerListReplaceRulesHandler(svc))
	g.POST("/replace-rules", readerCreateReplaceRuleHandler(svc))
	g.PATCH("/replace-rules/:id", readerUpdateReplaceRuleHandler(svc))
	g.DELETE("/replace-rules/:id", readerDeleteReplaceRuleHandler(svc))
}

func readerListSourcesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		sources, err := svc.Reader.ListSources(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"sources": sources})
	}
}

func readerImportSourcesHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Text string `json:"text" binding:"required"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		imported, err := svc.Reader.ImportSources(c.Request.Context(), userID, body.Text)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"imported": imported})
	}
}

func readerUpdateSourceHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil || body.Enabled == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "enabled 字段必填"})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.UpdateSourceEnabled(c.Request.Context(), userID, c.Param("id"), *body.Enabled); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerDeleteSourceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.DeleteSource(c.Request.Context(), userID, c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerDebugSourceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body struct {
			Key string `json:"key" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		logs, err := svc.Reader.Debug(c.Request.Context(), userID, c.Param("id"), body.Key)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"logs": logs})
	}
}

func readerSourceLoginInfoHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		info, err := svc.Reader.GetSourceLogin(c.Request.Context(), userID, c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, info)
	}
}

func readerSourceLoginActionHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		// Action 为 loginUi 里按钮的 action；留空表示执行 login()（确认登录）。
		Action string            `json:"action"`
		Fields map[string]string `json:"fields"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		res, err := svc.Reader.RunLoginAction(c.Request.Context(), userID, c.Param("id"), body.Action, body.Fields)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// readerBrowserPendingHandler 前端轮询：该用户在某书源下待用户完成的页面。
// 书源的 java.startBrowserAwait 会阻塞在服务端，前端据此把页面呈现出来。
func readerBrowserPendingHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		pages := svc.Reader.PendingBrowserPages(userID, c.Query("source_id"))
		c.JSON(http.StatusOK, gin.H{"pages": pages})
	}
}

// readerBrowserResultHandler 用户完成页面后回传 DOM（或取消），解除服务端阻塞。
func readerBrowserResultHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		ID        string `json:"id" binding:"required"`
		Body      string `json:"body"`
		URL       string `json:"url"`
		Cancelled bool   `json:"cancelled"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.ResolveBrowser(body.ID, userID, body.Body, body.URL, body.Cancelled); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// readerBrowserXHRHandler 转发承载页面内的接口请求。
//
// 页面在 iframe 里是不透明源，自己的 XHR 既带不上书源 Cookie 也会被 CORS 拦，
// 所以由父窗口（持 JWT）把请求转交到这里，服务端补上书源凭据再发。
func readerBrowserXHRHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		ID      string            `json:"id" binding:"required"`
		URL     string            `json:"url" binding:"required"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		res, err := svc.Reader.ProxyBrowserXHR(
			c.Request.Context(), body.ID, body.Method, body.URL, body.Headers, body.Body)
		if err != nil {
			// 交给页面自己处理失败，别把 4xx 泄成框架错误
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

// readerOpenCommentHandler 打开一条段评：复用书源登录态在宿主浏览器里承载评论页。
// 书源的 showCmt 内部就是「java.ajax 取评论页 → java.showBrowser 展示」，
// 这里用承载登录页的同一套机制等价实现（见 reader.OpenContentComment）。
func readerOpenCommentHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		BookID string `json:"book_id" binding:"required"`
		URL    string `json:"url" binding:"required"`
		Title  string `json:"title"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		page, err := svc.Reader.OpenContentComment(c.Request.Context(), userID, body.BookID, body.URL, body.Title)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, page)
	}
}

// readerBrowserPageHandler 承载待办页面本体。//
// 鉴权走 HMAC 签名而非 JWT：这个地址要填进 <iframe src>，而 iframe 的请求
// 带不上 Authorization 头。签名绑定待办 ID，链接随待办一起过期。
func readerBrowserPageHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		snap, err := svc.Reader.VerifyBrowserPage(c.Query("id"), c.Query("s"))
		if err != nil {
			c.String(http.StatusForbidden, "%s", err.Error())
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(snap.HTML))
	}
}

// readerBrowserAssetHandler 页面资源/表单/站内链接的同源代理。
// 服务端补上书源 Cookie 与请求头，使「用户后台」这类页面在 iframe 里保持登录态。
func readerBrowserAssetHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		target, _, err := svc.Reader.VerifyBrowserAsset(c.Query("id"), c.Query("u"), c.Query("s"))
		if err != nil {
			c.String(http.StatusForbidden, "%s", err.Error())
			return
		}
		contentType, status, data, err := svc.Reader.FetchBrowserAsset(c.Request.Context(), c.Query("id"), target)
		if err != nil {
			c.String(http.StatusBadGateway, "资源加载失败: %s", err.Error())
			return
		}
		if strings.Contains(strings.ToLower(contentType), "text/css") {
			data = []byte(svc.Reader.RewriteBrowserCSS(string(data), target, c.Query("id")))
		}
		c.Header("Cache-Control", "no-store")
		c.Data(status, contentType, data)
	}
}

func readerSourceLogoutHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.ClearSourceLogin(c.Request.Context(), userID, c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerSetSourceVariableHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Variable string `json:"variable"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.SetSourceVariable(c.Request.Context(), userID, c.Param("id"), body.Variable); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerSetSourceLoginInfoHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.SetSourceLoginInfo(c.Request.Context(), userID, c.Param("id"), body.Fields); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerSearchHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Key string `json:"key" binding:"required"`
		// SourceIDs 搜索范围：空表示全部启用书源（默认），非空则只搜这些书源。
		SourceIDs []string `json:"source_ids"`
		// Page 页码（从 1 开始）：书源 searchUrl 支持 {{page}} 时，前端滚到底逐页取。
		Page int `json:"page"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		books, skipped, err := svc.Reader.Search(c.Request.Context(), userID, body.Key, body.SourceIDs, body.Page)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"books": books, "skipped": skipped})
	}
}

func readerBookInfoHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		info, err := svc.Reader.GetBookInfo(
			c.Request.Context(),
			c.Query("source_id"), c.Query("source_url"), c.Query("book_url"),
		)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// 声明了封面解密的书源：详情封面也要走解密代理（尚未入库，按书源 URL 签）。
		if info != nil {
			// 顺手修复书架里被上游限流写坏的封面（存成接口地址的那种），先按原始
			// 地址写回，再做下发时的解密代理改写。
			userID := c.GetString(middleware.CtxUserID)
			svc.Reader.RepairShelfCover(c.Request.Context(), userID, c.Query("source_url"), c.Query("book_url"), info.CoverURL)
			info.CoverURL = svc.Reader.RewriteBookCover(c.Request.Context(), "", c.Query("source_url"), info.CoverURL)
		}
		c.JSON(http.StatusOK, info)
	}
}

func readerTocHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		chapters, err := svc.Reader.GetToc(
			c.Request.Context(),
			userID, c.Query("source_id"), c.Query("source_url"), c.Query("book_url"), c.Query("toc_url"),
		)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// nil 切片会被编码成 null，前端拿到 null 再取 .length 就是一句
		// 「Cannot read properties of null」。空目录统一给 []。
		if chapters == nil {
			chapters = []reader.TocChapter{}
		}
		c.JSON(http.StatusOK, gin.H{"chapters": chapters})
	}
}

func readerContentHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		content, err := svc.Reader.GetContent(
			c.Request.Context(),
			c.Query("source_id"), c.Query("source_url"), c.Query("book_url"), c.Query("chapter_url"),
		)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, content)
	}
}

func readerListBooksHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		books, err := svc.Reader.ListBooks(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 封面解密代理只在下发时改写：库里仍存原始地址，换源/换签名密钥后不会失效。
		for i := range books {
			books[i].CoverURL = svc.Reader.RewriteBookCover(c.Request.Context(), books[i].ID, books[i].Origin, books[i].CoverURL)
		}
		c.JSON(http.StatusOK, gin.H{"books": books})
	}
}

func readerAddBookHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Origin   reader.SearchOrigin `json:"origin" binding:"required"`
		Name     string              `json:"name" binding:"required"`
		Author   string              `json:"author"`
		CoverURL string              `json:"cover_url"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		book, err := svc.Reader.AddBook(c.Request.Context(), userID, body.Origin, body.Name, body.Author, body.CoverURL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// 后台补目录缓存，让书架能显示未读章数；失败不影响加入书架本身。
		svc.Reader.WarmUpBookChaptersAsync(c.Request.Context(), userID, book)
		c.JSON(http.StatusOK, book)
	}
}

// readerSwitchOriginHandler 换源：把书架里的书切到另一个书源。
// 阅读进度保留；旧源目录缓存清空后按新源后台重新预热。
func readerSwitchOriginHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Origin reader.SearchOrigin `json:"origin" binding:"required"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		book, err := svc.Reader.SwitchOrigin(c.Request.Context(), userID, c.Param("id"), body.Origin)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		svc.Reader.WarmUpBookChaptersAsync(c.Request.Context(), userID, book)
		c.JSON(http.StatusOK, book)
	}
}

// readerImportLocalBookHandler 上传本地书籍（TXT / EPUB）并加入书架。
// 正文落盘到 data/reader/local，目录切分后与网络书籍共用阅读器链路。
func readerImportLocalBookHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 先卡住请求体大小，避免超大文件把内存打满（多给 1MB 放 multipart 头）
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(reader.LocalBookMaxBytes)+(1<<20))
		header, err := c.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少上传文件（表单字段 file），或文件超过大小上限"})
			return
		}
		f, err := header.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传文件失败: " + err.Error()})
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, int64(reader.LocalBookMaxBytes)+1))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取上传文件失败: " + err.Error()})
			return
		}
		if len(data) > reader.LocalBookMaxBytes {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "文件超过大小上限"})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		book, err := svc.Reader.ImportLocalBook(c.Request.Context(), userID, header.Filename, data)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, book)
	}
}

// readerImportLocalBookFromPathHandler 从服务器已有文件导入书籍（TXT / EPUB），原地引用。
// 仅管理员：会读取服务器上允许根目录内的任意文件。
func readerImportLocalBookFromPathHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Path string `json:"path" binding:"required"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		abs, err := svc.FileManager.ResolvePath(body.Path)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		book, err := svc.Reader.ImportLocalBookFromPath(c.Request.Context(), userID, abs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, book)
	}
}

// readerImportLocalAudioDirHandler 把一个服务器目录导入为一本有声书，原地引用。
// 仅管理员；目录下的音频文件与 .strm 播放指针按相对路径排序成为章节。
func readerImportLocalAudioDirHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Path string `json:"path" binding:"required"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		abs, err := svc.FileManager.ResolvePath(body.Path)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		book, err := svc.Reader.ImportLocalAudioDir(c.Request.Context(), userID, abs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, book)
	}
}

// readerRefreshBooksTocHandler 更新目录：重抓书架里全部网络书籍的目录。
// 本地书籍与没有书源信息的书籍跳过；单本失败只计数，不影响其它书。
func readerRefreshBooksTocHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		res, err := svc.Reader.RefreshBooksToc(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, res)
	}
}

func readerRemoveBookHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.RemoveBook(c.Request.Context(), userID, c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// readerGetProfileHandler 读当前用户的阅读器偏好。
// 没保存过时返回 {"profile": null}，前端据此用本地值播种。
func readerGetProfileHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		settings, err := svc.Reader.GetReaderSettings(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profile": settings})
	}
}

// readerSaveProfileHandler 覆盖保存当前用户的阅读器偏好（服务端做范围收敛）。
func readerSaveProfileHandler(svc *service.Container) gin.HandlerFunc {
	var body reader.ReaderSettings
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		saved, err := svc.Reader.SaveReaderSettings(c.Request.Context(), userID, body)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profile": saved})
	}
}

// readerGetBookGroupsHandler 读当前用户的书架分组。
// 没有分组时返回空数组（不是 null），前端可以直接遍历。
func readerGetBookGroupsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		groups, err := svc.Reader.GetBookGroups(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"groups": groups})
	}
}

// readerSetBookGroupsHandler 覆盖保存当前用户的书架分组（整份替换）。
func readerSetBookGroupsHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Groups []model.BookGroupSet `json:"groups"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		groups, err := svc.Reader.SetBookGroups(c.Request.Context(), userID, body.Groups)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"groups": groups})
	}
}

// maxReaderChapterPos 进度上限：秒（听书）/ 页码（文本）/ 图片序号（漫画）都远小于它，
// 只用来挡住异常大的浮点数转 int 时溢出。约 115 天，足够覆盖任何单章。
const maxReaderChapterPos = 10_000_000

func readerSaveProgressHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		ChapterIndex int `json:"chapter_index"`
		// Pos 用 float64 接：听书的进度是 audio.currentTime（秒，天然带小数），
		// 漫画是图片序号、文本是页码（都是整数）。用 int 接小数会让整个请求
		// 400，而前端是 fire-and-forget，音频进度就被静默丢掉了。
		Pos          float64 `json:"pos"`
		ChapterTitle string  `json:"chapter_title"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// dur_chapter_pos 落库是 int，截断成秒；负数没有意义，夹到 0。
		posFloat := body.Pos
		if !(posFloat > 0) { // 同时挡住 0 与负数（JSON 不会给出 NaN）
			posFloat = 0
		}
		if posFloat > maxReaderChapterPos {
			posFloat = maxReaderChapterPos
		}
		pos := int(posFloat)
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.SaveProgress(c.Request.Context(), userID, c.Param("id"), body.ChapterIndex, pos, body.ChapterTitle); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// readerSaveAudioConfigHandler 保存听书片头/片尾跳过秒数（0 为不跳过）。
func readerSaveAudioConfigHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		OpenCredits  int `json:"open_credits"`
		CloseCredits int `json:"close_credits"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.SaveAudioConfig(
			c.Request.Context(), userID, c.Param("id"), body.OpenCredits, body.CloseCredits); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerListChaptersHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		chapters, err := svc.Reader.ListChapters(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"chapters": chapters})
	}
}

func readerReplaceChaptersHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Chapters []reader.ChapterInput `json:"chapters"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.Reader.SaveChapters(c.Request.Context(), c.Param("id"), body.Chapters); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerListReplaceRulesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		rules, err := svc.Reader.ListReplaceRules(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"rules": rules})
	}
}

func readerCreateReplaceRuleHandler(svc *service.Container) gin.HandlerFunc {
	var body reader.ReplaceRuleInput
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		rule, err := svc.Reader.CreateReplaceRule(c.Request.Context(), userID, body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rule)
	}
}

func readerUpdateReplaceRuleHandler(svc *service.Container) gin.HandlerFunc {
	var body reader.ReplaceRuleInput
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.UpdateReplaceRule(c.Request.Context(), userID, c.Param("id"), body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerDeleteReplaceRuleHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.DeleteReplaceRule(c.Request.Context(), userID, c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

// readerMediaProxyHandler 音频流/漫画图片签名代理：
// 校验 HMAC 签名 → 携书源防盗链头拉取 → Range 透传（音频拖动）/ m3u8 重写。
func readerMediaProxyHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		bookID := c.Query("b")
		rawURL, err := svc.Reader.VerifyProxyURL(bookID, c.Query("u"), c.Query("s"))
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 http(s) 媒体地址"})
			return
		}
		// 封面解密代理（d=cover）：签名主体可能是书源 URL（搜索结果尚未入库）。
		// 封面不读取书籍正文，因此不需要书籍记录；统一整段读取后解密下发。
		if c.Query("d") == "cover" {
			resp, fetchErr := svc.Reader.FetchCover(c.Request.Context(), bookID, rawURL)
			if fetchErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "封面拉取失败: " + fetchErr.Error()})
				return
			}
			defer resp.Body.Close()
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, readerImageDecodeMaxBytes+1))
			if readErr != nil || len(data) > readerImageDecodeMaxBytes {
				c.JSON(http.StatusBadGateway, gin.H{"error": "封面过大或读取失败"})
				return
			}
			decoded := svc.Reader.DecodeCoverBytes(c.Request.Context(), bookID, rawURL, data)
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				contentType = http.DetectContentType(decoded)
			}
			c.Data(resp.StatusCode, contentType, decoded)
			return
		}
		book, err := svc.Reader.GetBook(c.Request.Context(), bookID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "书籍不存在"})
			return
		}
		// 图片地址可自带 ",{headers:{...}}" 选项段：拆分后 headers 逐图应用，
		// 代理地址里只保留不带选项的地址（见 ProxyURL）。
		rawURL, mediaOptions, _ := readerrule.ParseMediaOptions(rawURL)

		// 远端音轨：整段落盘缓存，命中后不再回源。
		//
		// 听书源的音频直链签在腾讯音乐 CDN 上，跨境链路会间歇性返回空 401
		// 或直接 RST 连接。旧实现每次都把抖动原样交给播放器：要么是 502 的
		// JSON，要么是截断的 206，浏览器一律判成 MEDIA_ERR_SRC_NOT_SUPPORTED
		// （前端据此提示「格式不支持，请检查 ffmpeg」——其实完全无关）。
		// 缓存后只要首播成功一次，后续拖动进度、续播、重试都走本地文件。
		// 缓存目录不可用时退回旧的流式透传，不让配置问题直接变成「播不了」。
		if c.Request.Method == http.MethodGet &&
			reader.IsAudioMediaURL(rawURL, book.Type) &&
			svc.Reader.RemoteAudioCachePath(bookID, rawURL) != "" {
			handleCachedReaderAudio(c, svc, book, bookID, rawURL, mediaOptions.Headers)
			return
		}

		// 只有 200 的 GET 需要嗅探响应体（判播放列表 / 判错误页）；
		// 206 的体是二进制分片、HEAD 没有体，读了反而要额外拼回去。
		probeBody := c.Request.Method == http.MethodGet
		media, err := fetchReaderMedia(c.Request.Context(), svc, book, rawURL, c.GetHeader("Range"), mediaOptions.Headers, probeBody)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "媒体拉取失败: " + err.Error()})
			return
		}
		resp := media.resp
		defer resp.Body.Close()
		// 换过票时用新地址：m3u8 里的相对分片要按新地址解析。
		rawURL = media.url

		ct := resp.Header.Get("Content-Type")
		// 书源声明了 imageDecode 时，图片字节需要服务端二次解密。
		// 这类响应必须整段读取后处理，不再支持 Range（解密后长度会变）。
		if decodeJS := svc.Reader.ImageDecodeRule(c.Request.Context(), book); decodeJS != "" && !isMediaPlaylistRequest(resp) {
			sniffed, readErr := io.ReadAll(io.LimitReader(resp.Body, readerImageDecodeMaxBytes+1))
			if readErr != nil || len(sniffed) > readerImageDecodeMaxBytes {
				c.JSON(http.StatusBadGateway, gin.H{"error": "图片过大或读取失败"})
				return
			}
			sniffed = append(append([]byte(nil), media.head...), sniffed...)
			decoded, decErr := svc.Reader.DecodeImageBytes(c.Request.Context(), book, rawURL, false, sniffed)
			if decErr != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "图片解密失败: " + decErr.Error()})
				return
			}
			contentType := ct
			if contentType == "" {
				contentType = http.DetectContentType(decoded)
			}
			c.Data(resp.StatusCode, contentType, decoded)
			return
		}
		// 200 的 GET：media.head 已经读过一小段，先用它判播放列表与错误页。
		//
		// m3u8 判定不能只看 Content-Type 或 URL 后缀：上游经常把播放列表标成
		// application/octet-stream，或者地址是 /index.m3u8?token=...（后缀判断不命中），
		// 所以以 #EXTM3U 标记为准。
		if probeBody && resp.StatusCode == http.StatusOK {
			head := media.head
			if reader.BodyIsPlaylist(head, ct) {
				rest, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
				rewritten := svc.Reader.RewritePlaylist(bookID, rawURL, string(head)+string(rest))
				c.Data(http.StatusOK, "application/vnd.apple.mpegurl", []byte(rewritten))
				return
			}
			// 走到这里说明 head 不是错误页（fetchReaderMedia 已经把错误页换票重试过了），
			// 原样拼回去继续流式透传。Content-Length 因为已读过一部分会失配，
			// 直接省略交给 net/http 按 chunked 处理，避免少写/多写导致截断。
			for _, h := range []string{"Content-Type", "Content-Range", "Accept-Ranges"} {
				if v := resp.Header.Get(h); v != "" {
					c.Header(h, v)
				}
			}
			c.Status(resp.StatusCode)
			if len(head) > 0 {
				_, _ = c.Writer.Write(head)
			}
			streamReaderMediaBody(c.Writer, resp.Body)
			return
		}
		// 流式透传（含 206 Partial Content，支持音频拖动进度）
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
			if v := resp.Header.Get(h); v != "" {
				c.Header(h, v)
			}
		}
		c.Status(resp.StatusCode)
		streamReaderMediaBody(c.Writer, resp.Body)
	}
}

// streamReaderMediaBody 把上游响应体写到客户端，并在上游中途断开时**主动掐断**连接。
//
// 这是修复「播放器拿到半首歌」的关键：以前这里 `_, _ = io.Copy(...)` 把错误直接
// 丢掉，上游（腾讯 CDN 边缘）在跨境链路上 RST 连接时，客户端收到的是一个正常
// 结束、但字节数不足的响应 —— 播放器无从判断，只能报
// ERROR_CODE_PARSING_CONTAINER_UNSUPPORTED（看起来像「格式不支持」，实际是被截断）。
//
// 现在改成：上游没读完就中止这个响应，客户端因此拿到「传输不完整」而不是「正常结束」，
// 播放器可以按常规的重试/重连逻辑处理，而不是误判成格式问题。
func streamReaderMediaBody(w io.Writer, body io.Reader) {
	if _, err := io.Copy(w, body); err != nil {
		// panic(http.ErrAbortHandler) 是 net/http 约定的「异常中止」：不再补写
		// chunked 结尾，直接关闭连接，客户端据此判定响应不完整。
		panic(http.ErrAbortHandler)
	}
}

// ─── 远端音轨落盘缓存 ──────────────────────────────────────────────────────

const (
	// readerAudioCacheAttempts 首次落盘的最多尝试次数（每次内部都会走「失败换票」）。
	readerAudioCacheAttempts = 4
	// readerAudioCacheRetryDelay 重试退避基数，实际等待为 (attempt-1) 倍。
	readerAudioCacheRetryDelay = 500 * time.Millisecond
	// readerAudioCacheMaxBytes 单个音频的落盘上限。有声书一章约 3–5MB，
	// 给到 64MB 足够容纳长章节，也能挡住误当成音频的超大对象。
	readerAudioCacheMaxBytes = 64 << 20
	// readerMediaReissueAttempts 换票重试次数：跨境 CDN 的「空 401」是概率性的，
	// 一张票不行往往换一张就好；旧实现只换一次，第二次失败就把 502 交给播放器。
	readerMediaReissueAttempts = 3
	readerMediaReissueDelay    = 300 * time.Millisecond
)

// readerAudioFetchGroup 同一音轨并发请求（播放器 + 预取 + 预热）只下载一次。
var readerAudioFetchGroup singleflight.Group

// handleCachedReaderAudio 音频直链的统一入口：先查本地缓存，未命中则整段拉取落盘，
// 最后交给 http.ServeContent 处理 Range（拖动进度、续播都读本地文件）。
func handleCachedReaderAudio(
	c *gin.Context,
	svc *service.Container,
	book *model.ReaderBook,
	bookID, rawURL string,
	headers map[string]string,
) {
	if f, info, ok := svc.Reader.OpenCachedRemoteAudio(bookID, rawURL); ok {
		defer f.Close()
		serveCachedReaderAudio(c, f, info, rawURL)
		return
	}
	cachePath, err := ensureCachedRemoteAudio(c.Request.Context(), svc, book, bookID, rawURL, headers)
	if err != nil {
		if svc.Log != nil {
			svc.Log.Warn("reader 音频拉取失败", zap.String("book", bookID), zap.Error(err))
		}
		c.String(http.StatusBadGateway, "音频拉取失败: %s", err.Error())
		return
	}
	f, err := os.Open(cachePath) // #nosec G304 -- 路径由服务端哈希生成
	if err != nil {
		c.String(http.StatusBadGateway, "音频缓存不可用: %s", err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		c.String(http.StatusBadGateway, "音频缓存不可用: %s", err.Error())
		return
	}
	serveCachedReaderAudio(c, f, info, rawURL)
}

// serveCachedReaderAudio 用 http.ServeContent 下发本地音轨（自带 Range / 206 语义）。
func serveCachedReaderAudio(c *gin.Context, f *os.File, info os.FileInfo, rawURL string) {
	c.Header("Content-Type", readerAudioContentType(rawURL))
	c.Header("Cache-Control", "private, max-age=604800")
	http.ServeContent(c.Writer, c.Request, "audio"+readerAudioExt(rawURL), info.ModTime(), f)
}

// ensureCachedRemoteAudio 单飞下载：并发命中同一章时只回源一次。
func ensureCachedRemoteAudio(
	ctx context.Context,
	svc *service.Container,
	book *model.ReaderBook,
	bookID, rawURL string,
	headers map[string]string,
) (string, error) {
	key := svc.Reader.RemoteAudioCachePath(bookID, rawURL)
	if key == "" {
		return "", fmt.Errorf("音频缓存目录不可用")
	}
	v, err, _ := readerAudioFetchGroup.Do(key, func() (any, error) {
		if svc.Reader.HasCachedRemoteAudio(bookID, rawURL) {
			return key, nil
		}
		if err := downloadReaderAudioToCache(ctx, svc, book, bookID, rawURL, headers); err != nil {
			return nil, err
		}
		return key, nil
	})
	if err != nil {
		return "", err
	}
	path, _ := v.(string)
	if path == "" {
		return "", fmt.Errorf("音频缓存不可用")
	}
	return path, nil
}

// downloadReaderAudioToCache 整段拉取并落盘：上游断流/换票失败时重试，
// 落盘后按 Content-Length 校验长度，防止把截断的音频缓存下来。
func downloadReaderAudioToCache(
	ctx context.Context,
	svc *service.Container,
	book *model.ReaderBook,
	bookID, rawURL string,
	headers map[string]string,
) error {
	var lastErr error
	for attempt := 1; attempt <= readerAudioCacheAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(readerAudioCacheRetryDelay * time.Duration(attempt-1)):
			}
		}
		out, err := fetchReaderMediaResilient(ctx, svc, book, rawURL, "", headers, true)
		if err != nil {
			lastErr = err
			continue
		}
		if out == nil || out.resp == nil {
			lastErr = fmt.Errorf("音源响应为空")
			continue
		}
		resp := out.resp
		status := resp.StatusCode
		if status != http.StatusOK && status != http.StatusPartialContent {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("音源返回 %d", status)
			continue
		}
		expected := resp.ContentLength
		path, storeErr := svc.Reader.StoreCachedRemoteAudio(bookID, rawURL, func(w io.Writer) error {
			if len(out.head) > 0 {
				if _, err := w.Write(out.head); err != nil {
					return err
				}
			}
			return copyLimited(w, resp.Body, readerAudioCacheMaxBytes)
		})
		_ = resp.Body.Close()
		if storeErr != nil {
			lastErr = storeErr
			continue
		}
		if expected > 0 {
			info, statErr := os.Stat(path)
			if statErr != nil {
				_ = os.Remove(path)
				lastErr = fmt.Errorf("音频缓存写入后不可读: %w", statErr)
				continue
			}
			if info.Size() != expected {
				_ = os.Remove(path)
				lastErr = fmt.Errorf("音频写入不完整（%d/%d 字节）", info.Size(), expected)
				continue
			}
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("音源拉取失败")
	}
	return fmt.Errorf("已重试 %d 次: %w", readerAudioCacheAttempts, lastErr)
}

// copyLimited 复制响应体，超过上限时报错（避免把超大对象写进缓存）。
func copyLimited(w io.Writer, r io.Reader, limit int64) error {
	n, err := io.Copy(w, io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("音源超过 %d 字节上限", limit)
	}
	return nil
}

// readerAudioExt 取音频地址的后缀（用于推断 Content-Type）。
func readerAudioExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(path.Ext(u.Path))
}

// readerAudioContentType 按后缀给出 Content-Type：缓存文件统一存成 .audio，
// 不能依赖 http.ServeContent 的嗅探，直接在响应头里写死。
func readerAudioContentType(rawURL string) string {
	switch readerAudioExt(rawURL) {
	case ".mp3":
		return "audio/mpeg"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	default:
		// .m4a / .m4b / .wma / .ape 以及无后缀地址：腾讯的听书直链都是 m4a。
		return "audio/mp4"
	}
}

// readerImageDecodeMaxBytes 需要服务端解密的单张图片上限。
const readerImageDecodeMaxBytes = 32 << 20

// readerMediaProbeBytes 媒体代理嗅探上游响应体用的前缀长度。
//
// 这个前缀同时用于三件事：判定 HLS 播放列表（#EXTM3U）、判定「其实是错误页」、
// 以及把已读出的字节原样拼回响应体继续流式透传。
const readerMediaProbeBytes = 64 << 10

// readerMediaResult 一次媒体拉取的结果。
type readerMediaResult struct {
	resp *http.Response
	// url 是本次真正请求的媒体地址（换过票时是新票），m3u8 重写要用它做 base。
	url string
	// head 是已从响应体读出的前缀（仅 200 的 GET 才有），调用方写响应时要先写它。
	head []byte
	// rejected 表示 200 的响应体其实是错误页（HTML/JSON 报错），不能喂给播放器。
	rejected bool
}

// fetchReaderMedia 拉取媒体，并在「签名票失效」时换票重试一次。
//
// 听书源的播放地址是第三方中转现签的 CDN 直链（vkey/uid 带时效），票被上游判掉时
// 有两种表现，这里都要兜住：
//
//	① 空的 401：没有 Content-Type、没有 body（腾讯 CDN 对无效 vkey/uid 的固定回答）；
//	② 200 + HTML/JSON 错误页：中转自己出错时把它的报错页当成媒体回给我们。
//
// 旧行为是两种都原样透传给播放器：① 变成连续多个 401（播放器重试的是同一个
// 必然失败的地址），② 变成 ERROR_CODE_PARSING_CONTAINER_UNSUPPORTED（19 个
// extractor 一个都不认）。两者的根因都是「这张票已经不能用了」，所以统一处理成
// 回源换一张新票再拉：
//
//   - 换票成功 → 用新地址重拉，新票同时写回章节缓存，后续请求自然有效；
//   - 地址不属于可换票的音轨（漫画图片防盗链的 403 等）→ 原样透传上游状态，
//     不改变原有行为；
//   - 该换票但换不到、或换票后依然不可用 → 返回错误，由调用方给出明确文案，
//     而不是让播放器对着必然失败的地址反复重试。
func fetchReaderMedia(
	ctx context.Context,
	svc *service.Container,
	book *model.ReaderBook,
	rawURL, rangeHeader string,
	options map[string]string,
	probeBody bool,
) (*readerMediaResult, error) {
	// 上游 CDN 在并发/跨境链路上偶发 reset 连接（实测同一章重试即可成功），
	// 探测阶段读到一半断开不能直接判死，先按退避重试几次。
	out, refreshable, err := fetchReaderMediaWithRetry(ctx, svc, book, rawURL, rangeHeader, options, probeBody)
	if err != nil || !refreshable {
		return out, err
	}
	fresh, canRefresh, refreshErr := svc.Reader.RefreshMediaURL(ctx, book, rawURL)
	if !canRefresh {
		// 换不动（漫画图片防盗链的 403 等）：原样透传上游状态，保持既有行为。
		// 但有两类失败例外——它们透传给播放器毫无意义（播放器只会一直重试
		// 同一个必然失败的地址，或者把网页当成音频），改成明确错误让前端提示。
		switch {
		case out.rejected:
			_ = out.resp.Body.Close()
			return nil, fmt.Errorf("音源返回了非媒体内容（%s），请稍后重试或换源",
				strings.TrimSpace(out.resp.Header.Get("Content-Type")))
		case out.resp.StatusCode == http.StatusUnauthorized:
			_, _ = io.Copy(io.Discard, io.LimitReader(out.resp.Body, 1<<16))
			_ = out.resp.Body.Close()
			return nil, fmt.Errorf("音源签名失效（上游 401 拒绝），请稍后重试或换源")
		}
		return out, nil
	}
	// 换得动就必须先释放这张失败响应的连接，再发新请求。
	_, _ = io.Copy(io.Discard, io.LimitReader(out.resp.Body, 1<<16))
	_ = out.resp.Body.Close()
	if refreshErr != nil {
		return nil, fmt.Errorf("音源签名失效且重新取票失败: %w", refreshErr)
	}
	retried, _, retryErr := fetchReaderMediaOnce(ctx, svc, book, fresh, rangeHeader, options, probeBody)
	if retryErr != nil {
		return nil, retryErr
	}
	if retried.rejected {
		_ = retried.resp.Body.Close()
		return nil, fmt.Errorf("音源换票后仍返回非媒体内容，请稍后重试或换源")
	}
	if reader.MediaNeedsRefresh(retried.resp.StatusCode) {
		return nil, fmt.Errorf("音源换票后仍被拒绝（上游 %d），请稍后重试或换源", retried.resp.StatusCode)
	}
	return retried, nil
}

// fetchReaderMediaResilient 在 fetchReaderMedia（已含一次换票）之上再包一层。
//
// 跨境 CDN 判掉签名票是概率性的：换一张票往往就能取到，而旧实现只换一次，
// 第二次失败直接把 502 交给播放器。这里最多再重来 readerMediaReissueAttempts 轮，
// 每轮都会作废章节缓存、重新回源签票。不可恢复的 4xx（如 404）原样返回，
// 不做无意义的等待。
func fetchReaderMediaResilient(
	ctx context.Context,
	svc *service.Container,
	book *model.ReaderBook,
	rawURL, rangeHeader string,
	options map[string]string,
	probeBody bool,
) (*readerMediaResult, error) {
	var lastErr error
	for attempt := 1; attempt <= readerMediaReissueAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(readerMediaReissueDelay * time.Duration(attempt-1)):
			}
		}
		out, err := fetchReaderMedia(ctx, svc, book, rawURL, rangeHeader, options, probeBody)
		if err == nil {
			if out == nil || out.resp == nil {
				lastErr = fmt.Errorf("音源响应为空")
				continue
			}
			if !out.rejected && !reader.MediaNeedsRefresh(out.resp.StatusCode) {
				return out, nil
			}
			lastErr = fmt.Errorf("音源换票后仍不可用（上游 %d）", out.resp.StatusCode)
			_ = out.resp.Body.Close()
			continue
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// readerMediaProbeAttempts 探测阶段遇到连接中断（reset/EOF）的最大尝试次数。
//
// 上游 CDN（腾讯 CDN 的跨境边缘）在并发或长传输时会直接 RST 连接，同一章
// 重试一次往往就好。以前这里读 body 失败就直接回 502，把一次可恢复的抖动
// 变成了「这本书播不了」。
const readerMediaProbeAttempts = 3

// readerMediaProbeRetryDelay 探测重试的退避基数。
const readerMediaProbeRetryDelay = 300 * time.Millisecond

// readerMediaProbeError 探测阶段的可重试错误（连接被 reset / 提前 EOF）。
type readerMediaProbeError struct{ err error }

func (e *readerMediaProbeError) Error() string { return e.err.Error() }
func (e *readerMediaProbeError) Unwrap() error { return e.err }

// fetchReaderMediaWithRetry 探测阶段按退避重试，只对「连接中断」类错误重试：
// 上游明确回了 4xx 时重试同一张票没有意义（那是换票流程的事）。
func fetchReaderMediaWithRetry(
	ctx context.Context,
	svc *service.Container,
	book *model.ReaderBook,
	rawURL, rangeHeader string,
	options map[string]string,
	probeBody bool,
) (*readerMediaResult, bool, error) {
	var lastErr error
	for attempt := 1; attempt <= readerMediaProbeAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case <-time.After(readerMediaProbeRetryDelay * time.Duration(attempt-1)):
			}
		}
		out, refreshable, err := fetchReaderMediaOnce(ctx, svc, book, rawURL, rangeHeader, options, probeBody)
		if err == nil {
			return out, refreshable, nil
		}
		var probeErr *readerMediaProbeError
		if !errors.As(err, &probeErr) {
			return nil, false, err
		}
		lastErr = err
	}
	return nil, false, lastErr
}

// fetchReaderMediaOnce 发一次媒体请求并做可用性判定。
//
// 返回的 refreshable 表示「这次失败值得换一张签名票再试」：
// 上游 401/403/410，或 200 但响应体其实是错误页（详见 fetchReaderMedia 的说明）。
// rejected 表示「200 但内容不是媒体」——调用方据此在无法换票时报明确错误，
// 而不是把网页喂给播放器。
func fetchReaderMediaOnce(
	ctx context.Context,
	svc *service.Container,
	book *model.ReaderBook,
	rawURL, rangeHeader string,
	options map[string]string,
	probeBody bool,
) (*readerMediaResult, bool, error) {
	resp, err := svc.Reader.FetchMediaWithOptions(ctx, book, rawURL, rangeHeader, options)
	if err != nil {
		return nil, false, err
	}
	if reader.MediaNeedsRefresh(resp.StatusCode) {
		return &readerMediaResult{resp: resp, url: rawURL}, true, nil
	}
	if !probeBody || resp.StatusCode != http.StatusOK {
		return &readerMediaResult{resp: resp, url: rawURL}, false, nil
	}
	// 先读一小段判断是不是错误页/播放列表；不是就原样拼回去继续流式透传。
	head, readErr := io.ReadAll(io.LimitReader(resp.Body, readerMediaProbeBytes))
	if readErr != nil {
		_ = resp.Body.Close()
		// 连接被 reset / 提前 EOF 属于上游抖动，交给上层退避重试。
		return nil, false, &readerMediaProbeError{err: fmt.Errorf("读取媒体响应失败: %w", readErr)}
	}
	rejected := readerMediaPayloadRejected(head, resp.Header.Get("Content-Type"))
	return &readerMediaResult{resp: resp, url: rawURL, head: head, rejected: rejected}, rejected, nil
}

// readerMediaPayloadRejected 判断上游 200 响应体其实是错误页而不是媒体。
//
// 直链失效的另一个变种：CDN/中转不返回 4xx，而是回一段 HTML 错误页或 JSON
// 报错（Content-Type 是 text/html、application/json）。这类字节喂给播放器，
// ExoPlayer 的 19 个 extractor 全都不认，报出来的是
// ERROR_CODE_PARSING_CONTAINER_UNSUPPORTED —— 看起来像「格式不支持」，
// 实际是「拿到了一张网页」。这里按 Content-Type + 首字符识别出来，直接判失败。
func readerMediaPayloadRejected(head []byte, contentType string) bool {
	if len(head) == 0 {
		return false
	}
	lower := strings.ToLower(contentType)
	errish := strings.Contains(lower, "html") ||
		strings.Contains(lower, "json") ||
		strings.HasPrefix(lower, "text/")
	if !errish {
		return false
	}
	trimmed := bytes.TrimSpace(head)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case '<', '{', '[':
		return true
	default:
		return false
	}
}

// isMediaPlaylistRequest 判断响应是否可能已是 HLS 播放列表。
// 播放列表太小、不该走图片解密分支（书源的 imageDecode 只会用于图片）。
func isMediaPlaylistRequest(resp *http.Response) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(ct, "mpegurl")
}

// readerBodyIsPlaylist 判断上游响应体是否是 HLS 播放列表。
// 以 #EXTM3U 标记为准：Content-Type 只是辅助（上游常标成 octet-stream）。
func readerBodyIsPlaylist(head []byte, contentType string) bool {
	return reader.BodyIsPlaylist(head, contentType)
}

// readerLocalAssetHandler 本地书籍内嵌资源（EPUB 图片等）：
// 鉴权走 HMAC 签名（<img src> 带不上 JWT），与 /reader/media 同一套做法。
func readerLocalAssetHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		bookID := c.Query("b")
		entry, err := svc.Reader.VerifyLocalAssetURL(bookID, c.Query("p"), c.Query("s"))
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		data, contentType, err := svc.Reader.ReadLocalAsset(c.Request.Context(), bookID, entry)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		// 同一本书的图片不会变，可长缓存
		c.Header("Cache-Control", "private, max-age=604800")
		c.Data(http.StatusOK, contentType, data)
	}
}

// readerAudioTranscodeHandler 需要转码的有声书音轨：签名鉴权（<audio src> 带不上 JWT），
// 首次请求跑 ffmpeg 转成 mp3 落缓存，之后按 Range 下发，播放器可以拖动进度。
func readerAudioTranscodeHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		bookID := c.Query("b")
		source, err := svc.Reader.VerifyAudioTranscodeURL(bookID, c.Query("u"), c.Query("s"))
		if err != nil {
			c.String(http.StatusForbidden, "%s", err.Error())
			return
		}
		path, err := svc.Reader.EnsureTranscodedAudio(c.Request.Context(), bookID, source)
		if err != nil {
			c.String(http.StatusBadGateway, "%s", err.Error())
			return
		}
		f, err := os.Open(path) // #nosec G304 -- 路径由签名校验 + 缓存目录哈希生成
		if err != nil {
			c.String(http.StatusNotFound, "转码结果已丢失")
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			c.String(http.StatusNotFound, "转码结果不可用")
			return
		}
		c.Header("Cache-Control", "private, max-age=604800")
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
	}
}

// readerLocalAudioHandler 本地有声书音频流：签名鉴权（<audio src> 带不上 JWT），
// 交给 http.ServeContent 处理 Range，播放器才能拖动进度。
func readerLocalAudioHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		path, err := svc.Reader.VerifyLocalAudioURL(c.Query("b"), c.Query("p"), c.Query("s"))
		if err != nil {
			c.String(http.StatusForbidden, "%s", err.Error())
			return
		}
		f, info, err := svc.Reader.OpenLocalAudio(path)
		if err != nil {
			c.String(http.StatusNotFound, "%s", err.Error())
			return
		}
		defer f.Close()
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
	}
}

// readerBookContentHandler 书架维度正文（服务端应用替换净化规则）。
func readerBookContentHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		chapter, err := strconv.Atoi(c.DefaultQuery("chapter", "0"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "chapter 参数需为整数"})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		content, err := svc.Reader.GetContentForBook(c.Request.Context(), userID, c.Param("id"), chapter)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, content)
	}
}

// readerBookContentBatchHandler 批量取正文（对应 legado CacheBook / getContentBatch）。
//
// 请求 {chapter_indexes:[...], apply_replace:bool}：单章失败不中断整批，
// 返回每章内容与 hit/miss/failed 统计，供前端窗口预取与后续离线缓存。
func readerBookContentBatchHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		ChapterIndexes []int `json:"chapter_indexes" binding:"required"`
		// ApplyReplace 是否在服务端应用用户替换净化规则（默认 false，与单章接口一致）。
		ApplyReplace bool `json:"apply_replace"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "chapter_indexes 不能为空"})
			return
		}
		if len(body.ChapterIndexes) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "一次最多请求 64 章"})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		result, err := svc.Reader.GetContentBatch(c.Request.Context(), userID, c.Param("id"), body.ChapterIndexes, body.ApplyReplace)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
