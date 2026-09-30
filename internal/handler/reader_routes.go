// Package handler — 阅读（legado 书源兼容）子系统路由。
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
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

	// 搜索（多源聚合）
	g.POST("/search", readerSearchHandler(svc))

	// 详情 / 目录 / 正文
	g.GET("/book-info", readerBookInfoHandler(svc))
	g.GET("/toc", readerTocHandler(svc))
	g.GET("/content", readerContentHandler(svc))

	// 书架
	g.GET("/books", readerListBooksHandler(svc))
	g.POST("/books", readerAddBookHandler(svc))
	g.DELETE("/books/:id", readerRemoveBookHandler(svc))
	g.PUT("/books/:id/progress", readerSaveProgressHandler(svc))
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

func readerSearchHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		Key string `json:"key" binding:"required"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		books, skipped, err := svc.Reader.Search(c.Request.Context(), body.Key)
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
		chapters, err := svc.Reader.GetToc(
			c.Request.Context(),
			c.Query("source_id"), c.Query("source_url"), c.Query("book_url"), c.Query("toc_url"),
		)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
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
		c.JSON(http.StatusOK, book)
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

func readerSaveProgressHandler(svc *service.Container) gin.HandlerFunc {
	var body struct {
		ChapterIndex int    `json:"chapter_index"`
		Pos          int    `json:"pos"`
		ChapterTitle string `json:"chapter_title"`
	}
	return func(c *gin.Context) {
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID := c.GetString(middleware.CtxUserID)
		if err := svc.Reader.SaveProgress(c.Request.Context(), userID, c.Param("id"), body.ChapterIndex, body.Pos, body.ChapterTitle); err != nil {
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
