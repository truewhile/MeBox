package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/service"
)

type embyPlayingReq struct {
	ItemId        string `json:"ItemId"`
	ItemIDLower   string `json:"itemId"`
	ID            string `json:"Id"`
	IDLower       string `json:"id"`
	PositionTicks int64  `json:"PositionTicks"`
	PositionLower int64  `json:"positionTicks"`
	RunTimeTicks  int64  `json:"RunTimeTicks"`
	RunTimeLower  int64  `json:"runTimeTicks"`
	PlaySessionID string `json:"PlaySessionId"`
	PlaySession   string `json:"playSessionId"`
}

func embyPlayingProgressHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := embyUserID(c)
		if uid == "" {
			c.Status(http.StatusUnauthorized)
			return
		}
		var req embyPlayingReq
		_ = c.ShouldBindJSON(&req)
		itemID := embyFirstNonEmptyString(req.ItemId, req.ItemIDLower, req.ID, req.IDLower)
		if itemID == "" {
			itemID = embyFirstNonEmptyString(firstQueryValue(c, "ItemId", "itemId", "Id", "id"))
		}
		pos := req.PositionTicks
		if pos == 0 {
			pos = req.PositionLower
		}
		if pos == 0 {
			pos, _ = strconv.ParseInt(firstQueryValue(c, "PositionTicks", "positionTicks"), 10, 64)
		}
		runTime := req.RunTimeTicks
		if runTime == 0 {
			runTime = req.RunTimeLower
		}
		if runTime == 0 {
			runTime, _ = strconv.ParseInt(firstQueryValue(c, "RunTimeTicks", "runTimeTicks"), 10, 64)
		}
		playSessionID := embyFirstNonEmptyString(
			req.PlaySessionID,
			req.PlaySession,
			firstQueryValue(c, "PlaySessionId", "playSessionId"),
		)
		if itemID == "" {
			c.Status(http.StatusOK)
			return
		}
		clientInfo := embyClientInfoFromRequest(c)
		if svc.Device != nil && svc.Device.IsTerminalKicked(c.Request.Context(), uid, clientInfo.DeviceID, clientInfo.DeviceName, clientInfo.Client) {
			c.Status(http.StatusUnauthorized)
			return
		}
		if err := svc.Emby.RecordProgressWithSession(c.Request.Context(), uid, itemID, pos, runTime, playSessionID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		stopped := strings.Contains(strings.ToLower(c.FullPath()+" "+c.Request.URL.Path), "stopped")
		if svc.Sessions != nil {
			svc.Sessions.RecordPlayback(c.Request.Context(), uid, "",
				clientInfo.DeviceID,
				clientInfo.DeviceName,
				clientInfo.Client,
				c.ClientIP(),
				itemID,
				pos,
				runTime,
				stopped)
		}
		if svc.Device != nil && !stopped {
			svc.Device.RecordPlayback(c.Request.Context(), uid,
				clientInfo.DeviceID,
				clientInfo.DeviceName,
				clientInfo.Client)
		}
		c.Status(http.StatusNoContent)
	}
}

func embyFavoriteHandler(svc *service.Container, fav bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.Param("userId")
		if uid == "" {
			uid = embyUserID(c)
		}
		mid := c.Param("itemId")
		if uid == "" || mid == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := svc.Emby.SetFavorite(c.Request.Context(), uid, mid, fav); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out, _ := svc.Emby.Item(c.Request.Context(), mid, uid)
		if out != nil {
			c.JSON(http.StatusOK, out["UserData"])
			return
		}
		c.JSON(http.StatusOK, gin.H{"IsFavorite": fav})
	}
}

func embyMarkPlayedHandler(svc *service.Container, played bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.Param("userId")
		if uid == "" {
			uid = embyUserID(c)
		}
		mid := c.Param("itemId")
		if uid == "" || mid == "" {
			c.Status(http.StatusBadRequest)
			return
		}
		if err := svc.Emby.MarkPlayed(c.Request.Context(), uid, mid, played); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if played && svc.Device != nil {
			clientInfo := embyClientInfoFromRequest(c)
			svc.Device.RecordPlayback(c.Request.Context(), uid, clientInfo.DeviceID, clientInfo.DeviceName, clientInfo.Client)
		}
		out, _ := svc.Emby.Item(c.Request.Context(), mid, uid)
		if out != nil {
			c.JSON(http.StatusOK, out["UserData"])
			return
		}
		c.JSON(http.StatusOK, gin.H{"Played": played})
	}
}
