package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/truewhile/MeBox/internal/service"
)

func mediaQualityHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
		if page < 1 {
			page = 1
		} else if page > 1000000 {
			page = 1000000
		}
		if pageSize < 1 {
			pageSize = 50
		} else if pageSize > 100 {
			pageSize = 100
		}
		issue := c.DefaultQuery("issue", "unmatched")
		if issue != "unmatched" && issue != "poster" && issue != "missing" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未知检查分类"})
			return
		}
		items, total, err := svc.Media.QualityIssues(c.Request.Context(), issue, page, pageSize)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
	}
}
