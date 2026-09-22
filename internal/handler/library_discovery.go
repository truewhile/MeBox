package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// 媒体库筛选面板接口：facets 提供可选项，random 提供「随便看看」。
//
// 两者都走与列表完全相同的可见性判定（mediaVisibilityForRequest）与筛选解析
// （parseLibraryFilters），因此不会出现「列表里有、facets 里没有」或「随机跳
// 到了筛选条件之外的条目」这类不一致。

func libraryFacetsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		libraryID := c.Param("id")
		facets, err := svc.Media.LibraryFacets(
			c.Request.Context(),
			libraryID,
			mediaVisibilityForRequest(c, svc),
			svc.Discovery,
		)
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		c.JSON(http.StatusOK, facets)
	}
}

func libraryRandomHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		libraryID := c.Param("id")
		filters := parseLibraryFilters(c)
		// 随机只取一条，因此不带分页参数；未观看筛选仍需要会话用户。
		media, err := svc.Media.RandomMedia(
			c.Request.Context(),
			libraryID,
			mediaVisibilityForRequest(c, svc),
			filters,
		)
		if err != nil {
			writeInternalOrCanceled(c, err)
			return
		}
		if media == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no media matches the current filters"})
			return
		}
		c.JSON(http.StatusOK, media)
	}
}
