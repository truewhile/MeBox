package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
	"github.com/truewhile/MeBox/internal/service"
)

// TestListLibrariesRemotePreviewIsBounded 锁定远程库预览的总预算：
// 上游 Emby 卡住时，/api/libraries?with_preview=1 必须按时返回远程库条目
// （只是没有卡片），而不是跟着上游一起等十几秒。线上实测这个接口因此出现过
// 17.4 秒、30 秒（前端放弃）的响应，设置页也跟着整页空转。
func TestListLibrariesRemotePreviewIsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldBudget := remoteLibraryPreviewBudget
	remoteLibraryPreviewBudget = 200 * time.Millisecond
	t.Cleanup(func() { remoteLibraryPreviewBudget = oldBudget })

	// Views / 用户列表正常返回，Preview（Items 查询）永远不返回：模拟上游半挂。
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/Views"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id": "view-1", "Name": "远程动画", "CollectionType": "tvshows",
				}},
			})
			return
		case strings.HasSuffix(path, "/Users"):
			// api_key 账号要先解析出远程用户 Id，否则请求路径会落到兜底 "0"。
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "uid-1", "Name": "admin"}})
			return
		case r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "fake-token",
				"User":        map[string]any{"Id": "uid-1"},
			})
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer upstream.Close()
	defer close(release)

	// 唯一库名 + cache=shared：预览协程会并发读库（账号解析）。
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Library{}, &model.Media{}, &model.StrmAccount{}, &model.EmbyMount{}, &model.Setting{}, &model.User{}, &model.PlayProfile{}); err != nil {
		t.Fatal(err)
	}
	repos := repository.New(db)
	if err := db.Create(&model.User{Base: model.Base{ID: "admin-1"}, Username: "admin", Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	rawConfig, _ := json.Marshal(map[string]string{"url": upstream.URL, "token": "fake-token"})
	acct := model.StrmAccount{
		Base: model.Base{ID: "acct-1"}, Name: "远程Emby", Provider: model.StrmProviderEmbyRemote,
		Config: string(rawConfig), Enabled: true,
	}
	if err := repos.StrmAccount.Create(t.Context(), &acct); err != nil {
		t.Fatal(err)
	}
	mount := model.EmbyMount{
		Base: model.Base{ID: "mount-1"}, AccountID: acct.ID, RemoteViewID: "view-1",
		RemoteViewName: "远程动画", CollectionType: "tvshows", Enabled: true,
	}
	if err := repos.EmbyMount.Create(t.Context(), &mount); err != nil {
		t.Fatal(err)
	}
	svc := &service.Container{
		Repo:       repos,
		Media:      service.NewMediaService(&config.Config{}, zap.NewNop(), repos),
		EmbyRemote: service.NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, service.NewCryptoService("", zap.NewNop())),
		Cache:      service.NewRuntimeCacheService(&config.Config{}, zap.NewNop()),
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(middleware.CtxUserID, "admin-1")
	c.Set(middleware.CtxUserRole, "admin")
	c.Request = httptest.NewRequest(http.MethodGet, "/api/libraries?with_preview=1&include_hidden=1", nil)

	start := time.Now()
	listLibrariesHandler(svc)(c)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("request took %v with a hung upstream; the preview budget must bound it", elapsed)
	}
	var payload []webLibraryPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v (%s)", err, w.Body.String())
	}
	var remote *webLibraryPayload
	for i := range payload {
		if payload[i].IsRemoteEmby {
			remote = &payload[i]
			break
		}
	}
	if remote == nil {
		t.Fatalf("remote library missing from the bounded response: %#v", payload)
	}
	if len(remote.Cards) != 0 || remote.Total != 0 {
		t.Fatalf("expected the preview to be skipped on timeout, got total=%d cards=%d", remote.Total, len(remote.Cards))
	}
}
