// Package handler — DLNA / UPnP discovery + cast endpoints.
package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

func dlnaListHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		force := c.Query("force") == "true"
		devices, err := svc.DLNA.Discover(c.Request.Context(), force)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"devices": devices})
	}
}

type dlnaCastReq struct {
	ControlURL string `json:"control_url" binding:"required"`
	MediaURL   string `json:"media_url" binding:"required"`
}

func dlnaCastHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req dlnaCastReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// SSRF 防护：control_url 必须命中本服务发现到的真实渲染设备，
		// 防止登录用户借 cast 接口向任意内网地址发起 POST。
		// 优先用 30s 缓存；未命中时强制重扫一次再校验（设备可能刚上线）。
		devices, err := svc.DLNA.Discover(c.Request.Context(), false)
		if err == nil && !dlnaControlURLKnown(devices, req.ControlURL) {
			devices, err = svc.DLNA.Discover(c.Request.Context(), true)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if !dlnaControlURLKnown(devices, req.ControlURL) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown DLNA device: control_url must come from /api/dlna discovery"})
			return
		}
		if err := svc.DLNA.Cast(c.Request.Context(), req.ControlURL, req.MediaURL); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// dlnaControlURLKnown 判断 control_url 是否属于发现列表中的设备。
// 按解析后的 host:port+path 精确比对，容忍大小写与尾斜杠差异。
func dlnaControlURLKnown(devices []service.DLNADevice, controlURL string) bool {
	want, err := url.Parse(strings.TrimSpace(controlURL))
	if err != nil || want.Host == "" {
		return false
	}
	for _, dev := range devices {
		for _, candidate := range []string{dev.ControlURL, dev.Location} {
			u, err := url.Parse(strings.TrimSpace(candidate))
			if err != nil || u.Host == "" {
				continue
			}
			if strings.EqualFold(u.Host, want.Host) &&
				strings.EqualFold(strings.TrimRight(u.Path, "/"), strings.TrimRight(want.Path, "/")) {
				return true
			}
		}
	}
	return false
}
