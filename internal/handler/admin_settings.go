package handler

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
)

type settingReq struct {
	Key   string `json:"key" binding:"required"`
	Value string `json:"value"`
}

func listSettingsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings, err := svc.Repo.Setting.All(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, settings)
	}
}

func updateSettingHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req settingReq
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		oldValue := ""
		if req.Key == service.AdultLibraryIDsSettingKey {
			oldValue, _ = svc.Repo.Setting.Get(c.Request.Context(), req.Key)
		}
		if err := svc.Repo.Setting.Set(c.Request.Context(), req.Key, req.Value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		oldAdultLibraryIDs := service.DecodeAllowedLibraryIDs(oldValue)
		newAdultLibraryIDs := service.DecodeAllowedLibraryIDs(req.Value)
		if req.Key == service.AdultLibraryIDsSettingKey && len(oldAdultLibraryIDs) == 0 && len(newAdultLibraryIDs) > 0 {
			_ = svc.Repo.DB.WithContext(c.Request.Context()).Model(&model.User{}).Where("hide_adult = ?", false).Update("hide_adult", true).Error
		}
		service.ApplyRuntimeSetting(svc.Cfg, req.Key, req.Value)
		if err := applyHTTPSetting(svc, req.Key, req.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if svc.FFprobe != nil && (req.Key == "ffprobe.max_concurrent" || req.Key == "app.ffprobe_max_concurrent") {
			svc.FFprobe.SetMaxConcurrent(svc.Cfg.App.FFprobeMaxConcurrent)
		}
		if req.Key == "transcode.enabled" && !svc.Cfg.Transcoder.Enabled {
			svc.Transcoder.StopAll()
		}
		if req.Key == "transcode.hw_enabled" || req.Key == "transcode.hw_accel" || req.Key == "transcoder.hardware_accel" || req.Key == "transcoder.encoder" {
			svc.Transcoder.StopAll()
		}
		if req.Key == "cache.images_max_size_mb" && svc.Scheduler != nil {
			_ = svc.Scheduler.RunNowAsync(c.Request.Context(), "image_cache_cleanup")
		}
		if req.Key == "cache.memory_max_size_mb" && svc.Cache != nil {
			svc.Cache.SetMaxSizeMB(svc.Cfg.Cache.MemoryMaxSizeMB)
		}
		c.Status(http.StatusNoContent)
	}
}

// applyHTTPSetting 校验 HTTPS 相关设置，并在可行时热重载监听。
// 必须在 ApplyRuntimeSetting 之后调用，这样 svc.Cfg 已反映刚保存的值。
//
// 规则：
//   - https.enabled=true 时强制要求证书与私钥都已配置（内容或路径均可）且匹配，
//     否则返回错误（"如果启用就必须配置 SSL 证书和密钥"）；
//   - 证书/私钥（内容或路径）单独保存时只校验格式；若 HTTPS 已开启且新的整体
//     配置可解析匹配才触发重载，避免"只存了新证书、私钥还没保存"时用旧私钥带
//     新证书对外提供服务。
func applyHTTPSetting(svc *service.Container, key, value string) error {
	skipReload := func(reason string) {
		if svc.Log != nil {
			svc.Log.Warn("https setting saved but not applied yet", zap.String("key", key), zap.String("reason", reason))
		}
	}

	config.RuntimeMu.RLock()
	httpsEnabled := svc.Cfg.App.HTTPSEnabled
	cert := svc.Cfg.App.SSLCert
	certPath := svc.Cfg.App.SSLCertPath
	keyMaterial := svc.Cfg.App.SSLKey
	keyPath := svc.Cfg.App.SSLKeyPath
	config.RuntimeMu.RUnlock()

	switch key {
	case "https.enabled":
		if httpsEnabled {
			if _, err := service.ResolveSSLKeyPair(cert, certPath, keyMaterial, keyPath); err != nil {
				return fmt.Errorf("启用 HTTPS 失败：%v", err)
			}
		}
	case "https.cert", "https.cert_path", "https.key", "https.key_path":
		if err := validateSSLMaterialSource(key, value); err != nil {
			return err
		}
		if !httpsEnabled {
			return nil
		}
		if !httpsPairReady(svc) {
			skipReload("证书与私钥尚未匹配，等待另一半保存后生效")
			return nil
		}
	default:
		return nil
	}
	if svc.ReloadHTTPServer != nil {
		return svc.ReloadHTTPServer()
	}
	return nil
}

// validateSSLMaterialSource 校验刚保存的证书/私钥来源（内容或路径）本身格式合法。
func validateSSLMaterialSource(key, value string) error {
	switch key {
	case "https.cert":
		return service.ValidateSSLCert(value)
	case "https.cert_path":
		if strings.TrimSpace(value) == "" {
			return nil // 清空路径也允许，启用时由整体校验把关
		}
		pemStr, err := service.ResolveSSLMaterial("", value, "证书")
		if err != nil {
			return err
		}
		return service.ValidateSSLCert(pemStr)
	case "https.key":
		return service.ValidateSSLKey(value)
	case "https.key_path":
		if strings.TrimSpace(value) == "" {
			return nil
		}
		pemStr, err := service.ResolveSSLMaterial("", value, "私钥")
		if err != nil {
			return err
		}
		return service.ValidateSSLKey(pemStr)
	}
	return nil
}

// httpsPairReady 判断基于当前配置解析出的证书/私钥是否完整且匹配。
func httpsPairReady(svc *service.Container) bool {
	config.RuntimeMu.RLock()
	cert := svc.Cfg.App.SSLCert
	certPath := svc.Cfg.App.SSLCertPath
	keyMaterial := svc.Cfg.App.SSLKey
	keyPath := svc.Cfg.App.SSLKeyPath
	config.RuntimeMu.RUnlock()
	_, err := service.ResolveSSLKeyPair(cert, certPath, keyMaterial, keyPath)
	return err == nil
}

type testAdultScraperReq struct {
	Engine    string `json:"engine"`
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	JavDBURL  string `json:"javdb_url"`
	JavBusURL string `json:"javbus_url"`
	Cookie    string `json:"cookie"`
}

func testAdultScraperHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req testAdultScraperReq
		_ = c.ShouldBindJSON(&req)

		engine := strings.ToLower(strings.TrimSpace(req.Engine))
		if engine == "" {
			engine = "metatube"
		}

		if engine == "metatube" {
			serverURL := strings.TrimSpace(req.ServerURL)
			if serverURL == "" {
				serverURL, _ = svc.Repo.Setting.Get(c.Request.Context(), "adult.scraper.metatube_server")
			}
			if serverURL == "" {
				serverURL = "http://127.0.0.1:7700"
			}
			token := strings.TrimSpace(req.Token)
			if token == "" {
				token, _ = svc.Repo.Setting.Get(c.Request.Context(), "adult.scraper.metatube_token")
			}

			client := service.NewMetaTubeProvider(svc.Log)
			res, err := client.TestConnection(c.Request.Context(), serverURL, token)
			if err != nil {
				c.JSON(http.StatusOK, gin.H{
					"success":    false,
					"latency_ms": res.LatencyMs,
					"error":      err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"success":    res.Success,
				"latency_ms": res.LatencyMs,
				"providers":  res.Providers,
				"error":      res.Error,
			})
			return
		}

		// Builtin scraper test
		bases := []string{}
		if req.JavDBURL != "" {
			bases = append(bases, strings.TrimSpace(req.JavDBURL))
		}
		if req.JavBusURL != "" {
			bases = append(bases, strings.Split(req.JavBusURL, ",")...)
		}
		if len(bases) == 0 {
			if s, _ := svc.Repo.Setting.Get(c.Request.Context(), "adult.scraper.builtin_javdb_url"); s != "" {
				bases = append(bases, s)
			}
			if s, _ := svc.Repo.Setting.Get(c.Request.Context(), "adult.scraper.builtin_javbus_url"); s != "" {
				bases = append(bases, strings.Split(s, ",")...)
			}
		}
		if len(bases) == 0 {
			bases = []string{"https://javdb.com", "https://javbus.sbs", "https://www.javbus.com"}
		}

		cookie := strings.TrimSpace(req.Cookie)
		if cookie == "" {
			cookie, _ = svc.Repo.Setting.Get(c.Request.Context(), "adult.scraper.builtin_cookie")
		}
		if !strings.Contains(cookie, "age=") {
			cookie = cookie + "; age=verified"
		}

		httpClient := service.NewExternalHTTPClient(8 * time.Second)
		start := time.Now()
		var lastErr error
		success := false
		for _, b := range bases {
			b = strings.TrimRight(strings.TrimSpace(b), "/")
			if b == "" {
				continue
			}
			if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
				b = "https://" + b
			}
			httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, b, nil)
			if err != nil {
				lastErr = err
				continue
			}
			httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
			httpReq.Header.Set("Cookie", cookie)
			resp, err := httpClient.Do(httpReq)
			if err != nil {
				lastErr = err
				continue
			}
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
			_ = resp.Body.Close()
			if resp.StatusCode >= 400 {
				lastErr = fmt.Errorf("%s returned HTTP %d", b, resp.StatusCode)
				continue
			}
			text := string(bodyBytes)
			if strings.Contains(text, "driver-verify") || strings.Contains(text, "Age Verification") {
				lastErr = fmt.Errorf("%s intercepted by age verification", b)
				continue
			}
			success = true
			break
		}

		latency := time.Since(start).Milliseconds()
		if success {
			c.JSON(http.StatusOK, gin.H{
				"success":    true,
				"latency_ms": latency,
				"message":    "内置刮削源连接正常",
			})
			return
		}
		errMsg := "内置源连接失败"
		if lastErr != nil {
			errMsg = lastErr.Error()
		}
		c.JSON(http.StatusOK, gin.H{
			"success":    false,
			"latency_ms": latency,
			"error":      errMsg,
		})
	}
}

func recentLogsHandler(svc *service.Container) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := svc.Repo.Log.Recent(c.Request.Context(), 200)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rows)
	}
}
