// Package handler — 阅读（legado 书源兼容）子系统路由。
package handler

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
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

	// 替换净化规则
	g.GET("/replace-rules", readerListReplaceRulesHandler(svc))
	g.POST("/replace-rules", readerCreateReplaceRuleHandler(svc))
	g.PATCH("/replace-rules/:id", readerUpdateReplaceRuleHandler(svc))
	g.DELETE("/replace-rules/:id", readerDeleteReplaceRuleHandler(svc))
}

func readerListSourcesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		sources, err := svc.Reader.ListSources(c.Request.Context())
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
		imported, err := svc.Reader.ImportSources(c.Request.Context(), body.Text)
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
		if err := svc.Reader.UpdateSourceEnabled(c.Request.Context(), c.Param("id"), *body.Enabled); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerDeleteSourceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.Reader.DeleteSource(c.Request.Context(), c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func readerDebugSourceHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Key string `json:"key" binding:"required"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		logs, err := svc.Reader.Debug(c.Request.Context(), c.Param("id"), body.Key)
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
		if err := svc.Reader.ClearSourceLogin(c.Request.Context(), c.Param("id")); err != nil {
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
		if err := svc.Reader.SetSourceVariable(c.Request.Context(), c.Param("id"), body.Variable); err != nil {
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
		if err := svc.Reader.SetSourceLoginInfo(c.Request.Context(), c.Param("id"), body.Fields); err != nil {
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
		books, skipped, err := svc.Reader.Search(c.Request.Context(), body.Key, body.SourceIDs, body.Page)
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
		book, err := svc.Reader.GetBook(c.Request.Context(), bookID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "书籍不存在"})
			return
		}
		resp, err := svc.Reader.FetchMedia(c.Request.Context(), book, rawURL, c.GetHeader("Range"))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "媒体拉取失败: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		ct := resp.Header.Get("Content-Type")
		isPlaylist := strings.Contains(ct, "mpegurl") || strings.Contains(ct, "m3u8") ||
			strings.HasSuffix(strings.ToLower(rawURL), ".m3u8")
		if isPlaylist {
			// m3u8：改写分片/密钥地址为签名代理后返回，hls.js 无感续播
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			rewritten := svc.Reader.RewritePlaylist(bookID, rawURL, string(data))
			c.Data(http.StatusOK, "application/vnd.apple.mpegurl", []byte(rewritten))
			return
		}
		// 流式透传（含 206 Partial Content，支持音频拖动进度）
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
			if v := resp.Header.Get(h); v != "" {
				c.Header(h, v)
			}
		}
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
	}
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
