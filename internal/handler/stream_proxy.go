// 直连流的同源转发：网页端需要读取视频帧的场景（VR 全景渲染走 WebGL 纹理）
// 不能使用会跳到网盘 CDN 的跨域直链，这里把直链改为服务端转发。
//
// 只在客户端显式带上 ?proxy=1 时生效，普通播放仍走原来的 302 直连，
// 避免把网盘流量无谓地压到服务器上。
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

// wantSameOriginProxy 客户端是否要求把直连流改为服务端同源转发。
func wantSameOriginProxy(c *gin.Context) bool {
	if c == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(c.Query("proxy"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// proxySTRMStream 处理 STRM/网盘媒体的同源转发请求。返回 handled=false 表示
// 该媒体不需要（或无法）代理，调用方继续按原有逻辑处理。
func proxySTRMStream(c *gin.Context, svc *service.Container, m *model.Media) (bool, error) {
	if c == nil || svc == nil || svc.Strm == nil || m == nil {
		return false, nil
	}
	if !service.IsStrmMediaRow(m) {
		return false, nil
	}
	if err := svc.Strm.ProxyMediaDirect(c.Request.Context(), c.Writer, c.Request, m); err != nil {
		if errors.Is(err, service.ErrStrmProxyNotApplicable) {
			return false, nil
		}
		return true, err
	}
	return true, nil
}

// writeProxyError 在尚未写入任何响应内容时回一个明确的网关错误。
func writeProxyError(c *gin.Context, err error) {
	if c == nil || err == nil || c.Writer.Written() {
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
}
