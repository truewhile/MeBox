package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

// Telegram 绑定与测试接口。
//
// 绑定刻意做成「网页生成一次性码 → 用户在 Bot 里发 /bind <码>」：服务端不需要
// 用户手工填写 chat id，也不需要站点暴露 Bot 命令以外任何能力。

type telegramBindCodePayload struct {
	Code      string `json:"code"`
	ExpiresIn int    `json:"expires_in_seconds"`
}

// startTelegramBindHandler 生成一次性绑定码。同一用户重复调用时旧码作废。
func startTelegramBindHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc.Telegram == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram service unavailable"})
			return
		}
		userID := sessionUserID(c)
		code, err := svc.Telegram.StartBind(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, telegramBindCodePayload{Code: code, ExpiresIn: 300})
	}
}

// getTelegramStatusHandler 返回绑定状态与脱敏会话 ID。
func getTelegramStatusHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc.Telegram == nil {
			c.JSON(http.StatusOK, gin.H{"bound": false})
			return
		}
		bound, masked := svc.Telegram.Status(c.Request.Context(), sessionUserID(c))
		payload := gin.H{"bound": bound}
		if bound {
			payload["chat_id_masked"] = masked
		}
		c.JSON(http.StatusOK, payload)
	}
}

// unbindTelegramHandler 解除当前用户的 Telegram 绑定。
func unbindTelegramHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc.Telegram == nil {
			c.Status(http.StatusNoContent)
			return
		}
		if err := svc.Telegram.Unbind(c.Request.Context(), sessionUserID(c)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// testTelegramHandler 向管理员会话发送一条测试消息，用于验证 Token/会话 ID。
func testTelegramHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc.Telegram == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "telegram service unavailable"})
			return
		}
		if !svc.Telegram.Configured(c.Request.Context()) {
			c.JSON(http.StatusBadRequest, gin.H{
				"success": false,
				"error":   "请先启用 Telegram 通知并填写 Bot Token 与管理员 Chat ID",
			})
			return
		}
		if err := svc.Telegram.SendToAdminChecked(c.Request.Context(), "✅ MeBox 测试消息：通知通道工作正常。"); err != nil {
			c.JSON(http.StatusOK, gin.H{"success": false, "error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	}
}
