package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

type pinnedLibrariesReq struct {
	LibraryIDs []string `json:"library_ids"`
}

type libraryTagsReq struct {
	Tags []model.LibraryTagSet `json:"tags"`
}

func getLibraryTagsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		tags, err := svc.Profile.GetLibraryTags(c.Request.Context(), uid.(string))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if tags == nil {
			tags = []model.LibraryTagSet{}
		}
		c.JSON(http.StatusOK, gin.H{"tags": tags})
	}
}

func setLibraryTagsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req libraryTagsReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		uid, _ := c.Get(middleware.CtxUserID)
		tags, err := svc.Profile.SetLibraryTags(c.Request.Context(), uid.(string), req.Tags)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if tags == nil {
			tags = []model.LibraryTagSet{}
		}
		c.JSON(http.StatusOK, gin.H{"tags": tags})
	}
}

func getPinnedLibrariesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, _ := c.Get(middleware.CtxUserID)
		ids, err := svc.Profile.GetPinnedLibraryIDs(c.Request.Context(), uid.(string))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if ids == nil {
			ids = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"library_ids": ids})
	}
}

func setPinnedLibrariesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req pinnedLibrariesReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		uid, _ := c.Get(middleware.CtxUserID)
		ids, err := svc.Profile.SetPinnedLibraryIDs(c.Request.Context(), uid.(string), req.LibraryIDs)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if ids == nil {
			ids = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"library_ids": ids})
	}
}
