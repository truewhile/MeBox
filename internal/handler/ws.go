// Package handler — WebSocket endpoint.
//
// Clients connect to /api/ws?token=... (the token is the same JWT used for
// REST calls). The first message they send is a JSON {"action":"subscribe",
// "topics":["scan","scrape","transcode"]}. Subsequent server-pushed events
// arrive as {"topic":"...","payload":{...}}.
package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/truewhile/MeBox/internal/service"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// 同源校验：浏览器跨站页面虽读不到 ?token=，但可能借 cookie 通道
	// （extractToken 接受 msgo_access_token cookie）发起跨站 WebSocket
	// 劫持。放行同源与非浏览器客户端（不发 Origin 头的 App/脚本），
	// 拒绝跨站 Origin。
	CheckOrigin: func(r *http.Request) bool {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return strings.EqualFold(u.Host, r.Host)
	},
}

func wsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		id := uuid.NewString()
		sub := svc.WSHub.Subscribe(id, nil)
		defer svc.WSHub.Unsubscribe(id)

		// Reader: accept ping + subscription updates.
		go func() {
			for {
				if _, _, err := conn.NextReader(); err != nil {
					_ = conn.Close()
					return
				}
			}
		}()

		// Writer: drain hub events into the socket with a periodic ping.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case ev, ok := <-sub.Out:
				if !ok {
					return
				}
				data, _ := json.Marshal(ev)
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}
}
