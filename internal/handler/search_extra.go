// Package handler — unified search surface that mirrors the Python
// project's /api/search* endpoints. Internally we delegate to the
// existing media + site adapters; advanced/tmdb/sites variants exist
// so the upstream Vue UI's queries don't need rewriting.
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// searchUnifiedHandler is the basic /api/search endpoint.
func searchUnifiedHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		q := c.Query("q")
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
		if limit <= 0 || limit > 200 {
			limit = 30
		}
		visibility := mediaVisibilityForRequest(c, svc)
		items, err := svc.Media.SearchMediaVisible(ctx, q, limit, visibility)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if svc.EmbyRemote != nil && strings.TrimSpace(q) != "" {
			if remoteItems, _ := svc.EmbyRemote.RemoteSearchMedia(ctx, q, limit, visibility); len(remoteItems) > 0 {
				items = append(items, remoteItems...)
				if len(items) > limit {
					items = items[:limit]
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
	}
}

// searchAdvancedHandler accepts query + optional filters
// (year, type, library_id) — currently it ignores the filters in the
// SQL but threads them through to the response so the UI can echo
// them back. This keeps API parity without a giant query builder.
func searchAdvancedHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		q := c.Query("q")
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
		if limit <= 0 || limit > 200 {
			limit = 30
		}
		visibility := mediaVisibilityForRequest(c, svc)
		items, err := svc.Media.SearchMediaVisible(ctx, q, limit, visibility)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if svc.EmbyRemote != nil && strings.TrimSpace(q) != "" {
			if remoteItems, _ := svc.EmbyRemote.RemoteSearchMedia(ctx, q, limit, visibility); len(remoteItems) > 0 {
				items = append(items, remoteItems...)
				if len(items) > limit {
					items = items[:limit]
				}
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"items": items,
			"filters": gin.H{
				"year":       c.Query("year"),
				"type":       c.Query("type"),
				"library_id": c.Query("library_id"),
			},
		})
	}
}

// searchTMDbHandler proxies the TMDb /search endpoint via the existing
// SearchMovie helper. Movies and TV use different URLs upstream but
// only the movie path is wired today; TV is best-effort.
func searchTMDbHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("query")
		if q == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "query required"})
			return
		}
		if svc.TMDb == nil || !svc.TMDb.Enabled() {
			c.JSON(http.StatusOK, gin.H{"items": []any{}, "note": "tmdb disabled"})
			return
		}
		match, err := svc.TMDb.SearchMovie(c.Request.Context(), q, 0)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out := make([]any, 0, 1)
		if match != nil {
			out = append(out, match)
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}
