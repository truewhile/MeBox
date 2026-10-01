// Package handler — Emby / Jellyfin 媒体分段（片头、片尾）兼容接口。
//
//	GET /MediaSegments/{itemId}
//	GET /Items/{itemId}/MediaSegments
//	GET /Users/{userId}/Items/{itemId}/MediaSegments
//
// 契约对齐 Jellyfin 10.10 引入的 Media Segments API（Emby 采用同一形状），
// 也是 TheIntroDB 官方 Jellyfin 插件走的同一条路：
//
//	QueryResult<MediaSegmentDto> = {"Items": [...], "TotalRecordCount": N}
//	MediaSegmentDto = {"Id", "ItemId", "Type", "StartTicks", "EndTicks"}
//
// 时间是 .NET ticks（1 tick = 100ns，即每秒 10,000,000、每毫秒 10,000）；
// Type 是枚举名字符串 Intro / Outro / Recap / Preview / Commercial。
package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// 第三方客户端会在起播前后同步请求分段，不能被一次外网抓取无限拖住。超时后
// 退回已有缓存（可能为空），请求本身永远不失败。
const embyMediaSegmentsFetchBudget = 5 * time.Second

// 1 秒 = 10,000,000 ticks => 1 毫秒 = 10,000 ticks。
const embyTicksPerMillisecond int64 = 10_000

func embyMediaSegmentsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 未知条目、虚拟剧集/季、远程 Emby 挂载、以及当前不可见的内容一律返回
		// 空结果而不是 404：客户端会把 404 判成「条目损坏」（同 emby_routes.go
		// 里 AdditionalParts 的说明），而「没有可跳过的片段」本来就是个合法状态。
		empty := gin.H{"Items": []any{}, "TotalRecordCount": 0}
		if svc == nil || svc.Repo == nil || svc.Segments == nil {
			c.JSON(http.StatusOK, empty)
			return
		}
		id := c.Param("id")
		// 远程 Emby 挂载的条目是上游库的投影，本地没有可查询的外部 ID 关联。
		if service.IsEmbyRemoteID(id) {
			c.JSON(http.StatusOK, empty)
			return
		}
		m, err := svc.Repo.Media.FindByID(c.Request.Context(), id)
		if err != nil || m == nil || !mediaVisibleForRequest(c, svc, m) {
			c.JSON(http.StatusOK, empty)
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), embyMediaSegmentsFetchBudget)
		defer cancel()
		rows, listErr := svc.Segments.ListForPlayback(ctx, m)
		if listErr != nil && svc.Log != nil {
			svc.Log.Debug("emby media segments lookup failed",
				zap.String("media_id", m.ID), zap.Error(listErr))
		}

		items := embySegmentItems(m, rows, embyRequestedSegmentTypes(c))
		c.JSON(http.StatusOK, gin.H{"Items": items, "TotalRecordCount": len(items)})
	}
}

// embySegmentItems 把库内片段转换成 MediaSegmentDto 列表。
func embySegmentItems(m *model.Media, rows []model.MediaSegment, want map[string]bool) []gin.H {
	// 末段在库内用 end_ms = 0 表示「一直到片尾」（TheIntroDB 对片尾返回 end_ms: null），
	// 这里必须换算成真实结束时间；拿不到时长就丢弃该分段，否则会给出一个零长度区间，
	// 客户端要么忽略要么画出一个错误的跳转点。
	durationMs := int64(m.DurationSec) * 1000
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		typeName := embySegmentTypeName(row.Kind)
		if typeName == "" {
			continue
		}
		if len(want) > 0 && !want[typeName] {
			continue
		}
		endMs := row.EndMs
		if endMs <= 0 {
			if durationMs <= 0 {
				continue
			}
			endMs = durationMs
		}
		if endMs <= row.StartMs {
			continue
		}
		items = append(items, gin.H{
			"Id":         row.ID,
			"ItemId":     m.ID,
			"Type":       typeName,
			"StartTicks": row.StartMs * embyTicksPerMillisecond,
			"EndTicks":   endMs * embyTicksPerMillisecond,
		})
	}
	return items
}

// embySegmentTypeName 把库内 kind 映射成 Emby/Jellyfin 的 MediaSegmentType 名字。
// 库内的 credits 取自 TheIntroDB 的字段名，在 Emby 一侧对应 Outro。
func embySegmentTypeName(kind string) string {
	switch kind {
	case model.SegmentKindIntro:
		return "Intro"
	case model.SegmentKindRecap:
		return "Recap"
	case model.SegmentKindCredits:
		return "Outro"
	case model.SegmentKindPreview:
		return "Preview"
	default:
		return ""
	}
}

// embyRequestedSegmentTypes 解析 includeSegmentTypes（Jellyfin 的过滤参数）。
// 支持重复参数与逗号分隔两种写法；返回空集合表示不过滤。
//
// 只认枚举名字符串。数字枚举虽然 ASP.NET 模型绑定也接受，但各家定义的顺序并
// 不一致，猜错会把过滤结果算错；认不出来时按「不过滤」处理，返回的是超集，
// 客户端自己仍会再过滤一次。
func embyRequestedSegmentTypes(c *gin.Context) map[string]bool {
	raw := make([]string, 0, 4)
	for _, key := range []string{"includeSegmentTypes", "IncludeSegmentTypes", "includesegmenttypes"} {
		raw = append(raw, c.QueryArray(key)...)
	}
	want := make(map[string]bool, len(raw))
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			for _, name := range []string{"Intro", "Outro", "Recap", "Preview", "Commercial"} {
				if strings.EqualFold(part, name) {
					want[name] = true
				}
			}
		}
	}
	return want
}
