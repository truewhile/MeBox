package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// Emby 发现类接口的 handler：NextUp / Similar / Genres。
//
// 这三个接口此前返回空列表，导致第三方客户端首页「接下来播放」、详情页
// 「相似推荐」、按类型浏览全部为空白。它们必须始终返回 200 + 合法信封，
// 因为客户端在首页刷新时会并发请求，任何 4xx/5xx 都会被判定为服务端异常。

func embyNextUpHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := embyScopedUserID(c)
		if userID == "" {
			c.JSON(http.StatusOK, embyEmptyItemsPayload())
			return
		}
		limit, _ := strconv.Atoi(embyFirstNonEmptyString(firstQueryValue(c, "Limit", "limit"), ""))
		out, err := svc.Emby.NextUp(c.Request.Context(), userID, limit)
		if err != nil {
			c.JSON(http.StatusOK, embyEmptyItemsPayload())
			return
		}
		embyAttachRequestTokenToMediaSources(c, out)
		c.JSON(http.StatusOK, out)
	}
}

func embySimilarHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		mediaID := strings.TrimSpace(c.Param("id"))
		if mediaID == "" {
			c.JSON(http.StatusOK, embyEmptyItemsPayload())
			return
		}
		limit, _ := strconv.Atoi(embyFirstNonEmptyString(firstQueryValue(c, "Limit", "limit"), ""))
		out, err := svc.Emby.SimilarItems(c.Request.Context(), mediaID, embyEffectiveUserID(c), limit)
		if err != nil {
			c.JSON(http.StatusOK, embyEmptyItemsPayload())
			return
		}
		embyAttachRequestTokenToMediaSources(c, out)
		c.JSON(http.StatusOK, out)
	}
}

func embyGenresHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		parentID := firstQueryValue(c, "ParentId", "parentId", "parentid")
		out, err := svc.Emby.Genres(c.Request.Context(), embyEffectiveUserID(c), parentID)
		if err != nil {
			c.JSON(http.StatusOK, embyEmptyItemsPayload())
			return
		}
		c.JSON(http.StatusOK, out)
	}
}

// embyScopedUserID 解析「按用户请求」的 Emby 接口的生效用户。
//
// 路由上带 :userId 时（/Users/{uid}/Shows/NextUp），只允许查询自己：客户端
// 偶尔会带着别人的 id 请求，直接采信等于开放他人观看历史的读取。管理员同样
// 按自己处理，避免出现一条无人使用的越权路径。
func embyScopedUserID(c *gin.Context) string {
	caller := embyEffectiveUserID(c)
	requested := strings.TrimSpace(c.Param("userId"))
	if requested == "" {
		return caller
	}
	if requested == caller {
		return caller
	}
	return ""
}

// embyEmptyItemsPayload 与 embyEmptyItemsHandler 保持同一形状。
func embyEmptyItemsPayload() gin.H {
	return gin.H{"Items": []any{}, "TotalRecordCount": 0}
}
