package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// 设备管理接口。
//
// 普通用户只能操作自己的设备（路由挂在 /me 下，用户 ID 始终取自会话）；
// 管理员通过 /admin/users/:id/devices 代管任意用户。两组接口共用同一份
// DeviceService，因此「谁上线过、谁被踢掉」只有一处事实来源。

// deviceListPayload 是设备列表的下发形状。Fingerprint 不外发：它是防共享
// 判定用的内部标识，暴露出去只会方便伪造。
type deviceListPayload struct {
	Devices []devicePayload `json:"devices"`
}

type devicePayload struct {
	ID         string `json:"id"`
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name,omitempty"`
	Client     string `json:"client,omitempty"`
	LastIP     string `json:"last_ip,omitempty"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	LastPlayAt string `json:"last_play_at,omitempty"`
	Kicked     bool   `json:"kicked"`
	Online     bool   `json:"online"`
	Playing    bool   `json:"playing"`
	Warnings   int    `json:"warnings"`
}

func toDevicePayload(d model.UserDevice) devicePayload {
	out := devicePayload{
		ID:         d.ID,
		DeviceID:   d.DeviceID,
		DeviceName: d.DeviceName,
		Client:     d.Client,
		LastIP:     d.LastIP,
		Kicked:     d.Kicked,
		Online:     d.Online,
		Playing:    d.Playing,
		Warnings:   d.Warnings,
	}
	if !d.LastSeenAt.IsZero() {
		out.LastSeenAt = d.LastSeenAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if d.LastPlayAt != nil && !d.LastPlayAt.IsZero() {
		out.LastPlayAt = d.LastPlayAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func deviceListResponse(devices []model.UserDevice) deviceListPayload {
	items := make([]devicePayload, 0, len(devices))
	for _, d := range devices {
		items = append(items, toDevicePayload(d))
	}
	return deviceListPayload{Devices: items}
}

// myDevicesHandler 返回当前会话用户的设备列表。
func myDevicesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := sessionUserID(c)
		devices, err := svc.Device.ListDevices(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, deviceListResponse(devices))
	}
}

// myKickDeviceHandler 踢掉当前用户的一台设备。
func myKickDeviceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := sessionUserID(c)
		deviceID := strings.TrimSpace(c.Param("deviceID"))
		if deviceID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "device id required"})
			return
		}
		if err := svc.Device.KickDevice(c.Request.Context(), userID, deviceID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// myKickAllDevicesHandler 踢掉当前用户的全部设备。
func myKickAllDevicesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := sessionUserID(c)
		if err := svc.Device.KickAllDevices(c.Request.Context(), userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// adminUserDevicesHandler 返回指定用户的设备列表。
func adminUserDevicesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := strings.TrimSpace(c.Param("id"))
		// FindByID 对「不存在」返回 (nil, nil)，必须判空而不是判 error，
		// 否则「用户不存在」会伪装成「该用户没有设备」的空列表。
		user, err := svc.Repo.User.FindByID(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if user == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		devices, err := svc.Device.ListDevices(c.Request.Context(), userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, deviceListResponse(devices))
	}
}

// adminKickUserDeviceHandler 由管理员踢掉指定用户的一台设备。
func adminKickUserDeviceHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := strings.TrimSpace(c.Param("id"))
		deviceID := strings.TrimSpace(c.Param("deviceID"))
		if deviceID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "device id required"})
			return
		}
		if err := svc.Device.KickDevice(c.Request.Context(), userID, deviceID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// adminKickAllUserDevicesHandler 由管理员踢掉指定用户的全部设备。
func adminKickAllUserDevicesHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := strings.TrimSpace(c.Param("id"))
		if err := svc.Device.KickAllDevices(c.Request.Context(), userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// sessionUserID 读取会话用户 ID。调用方路由都挂在鉴权中间件之后，因此这里
// 只做类型断言兜底，不做权限判断。
func sessionUserID(c *gin.Context) string {
	if v, ok := c.Get(middleware.CtxUserID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
