package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/truewhile/MeBox/internal/middleware"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/service"
	"github.com/truewhile/MeBox/internal/service/reader"
)

// 阅读器偏好接口（/reader/profile）的 HTTP 契约测试。
//
// 这些设置原先只存在浏览器 localStorage（设备级），换设备就丢。现在按用户落库，
// 所以要保证：新用户没有记录、写读一致、越界值收敛、以及用户之间互不影响。

// profileRouterForTest 用请求头 X-Test-User 指定调用者（默认 u1），
// 便于在一个测试里切换用户验证隔离。
func profileRouterForTest(t *testing.T, cfg *service.Container) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		user := c.GetHeader("X-Test-User")
		if user == "" {
			user = "u1"
		}
		c.Set(middleware.CtxUserID, user)
		c.Next()
	})
	registerReaderRoutes(r.Group("/api"), cfg)
	return r
}

func getProfile(t *testing.T, router *gin.Engine, user string) (*reader.ReaderSettings, int) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/reader/profile", nil)
	req.Header.Set("X-Test-User", user)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	var res struct {
		Profile *reader.ReaderSettings `json:"profile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return res.Profile, w.Code
}

func putProfile(t *testing.T, router *gin.Engine, user, body string) (*reader.ReaderSettings, int) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/reader/profile", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", user)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	var res struct {
		Profile *reader.ReaderSettings `json:"profile"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败: %v body=%s", err, w.Body.String())
	}
	return res.Profile, w.Code
}

func TestReaderProfileNullBeforeFirstSave(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	profile, code := getProfile(t, router, "u1")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	// 没有记录时返回 null，由前端用它本地的值播种
	if profile != nil {
		t.Fatalf("新用户应返回 null，实际 %+v", profile)
	}
}

func TestReaderProfileRoundTrip(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	body := `{
		"theme_id":"preset2","night":true,"page_mode":"scroll",
		"font_size":24,"line_height":2.1,"paragraph_spacing":12,
		"audio_speed":1.5,"audio_timer_minutes":30,
		"shelf_layout":"compact","shelf_grid_columns":4,"shelf_sort":"update",
		"shelf_show_unread":false,"shelf_show_update_time":true
	}`
	saved, code := putProfile(t, router, "u1", body)
	if code != http.StatusOK {
		t.Fatalf("保存失败 status=%d", code)
	}
	if saved == nil {
		t.Fatal("保存应返回偏好")
	}

	got, code := getProfile(t, router, "u1")
	if code != http.StatusOK || got == nil {
		t.Fatalf("读回失败 status=%d profile=%+v", code, got)
	}
	if *got != *saved {
		t.Fatalf("读回与写入不一致:\n写入 %+v\n读回 %+v", *saved, *got)
	}
	if got.ThemeID != "preset2" || !got.Night || got.PageMode != "scroll" || got.FontSize != 24 ||
		got.LineHeight != 2.1 || got.ParagraphSpacing != 12 || got.AudioSpeed != 1.5 ||
		got.AudioTimerMinutes != 30 || got.ShelfLayout != "compact" || got.ShelfGridColumns != 4 ||
		got.ShelfSort != "update" || got.ShelfShowUnread || !got.ShelfShowUpdateTime {
		t.Fatalf("字段未按原值往返: %+v", *got)
	}
}

func TestReaderProfileClampsAndFallsBack(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	// 全部越界 / 枚举非法，服务端应收敛而不是原样入库
	body := `{
		"theme_id":"   ","night":false,"page_mode":"nope",
		"font_size":999,"line_height":0.1,"paragraph_spacing":-5,
		"audio_speed":99,"audio_timer_minutes":9999,
		"shelf_layout":"diagonal","shelf_grid_columns":42,"shelf_sort":"random",
		"shelf_show_unread":true,"shelf_show_update_time":false
	}`
	got, code := putProfile(t, router, "u1", body)
	if code != http.StatusOK {
		t.Fatalf("保存失败 status=%d", code)
	}
	if got.ThemeID != "preset1" {
		t.Fatalf("空主题应回落到 preset1，实际 %q", got.ThemeID)
	}
	if got.PageMode != "page" {
		t.Fatalf("非法 page_mode 应回落到 page，实际 %q", got.PageMode)
	}
	if got.FontSize != 32 {
		t.Fatalf("font_size 应夹到上限 32，实际 %d", got.FontSize)
	}
	if got.LineHeight != 1.4 {
		t.Fatalf("line_height 应夹到下限 1.4，实际 %v", got.LineHeight)
	}
	if got.ParagraphSpacing != 0 {
		t.Fatalf("paragraph_spacing 应夹到下限 0，实际 %d", got.ParagraphSpacing)
	}
	if got.AudioSpeed != 3 {
		t.Fatalf("audio_speed 应夹到上限 3，实际 %v", got.AudioSpeed)
	}
	if got.AudioTimerMinutes != 180 {
		t.Fatalf("audio_timer_minutes 应夹到上限 180，实际 %d", got.AudioTimerMinutes)
	}
	if got.ShelfLayout != "grid" {
		t.Fatalf("非法 shelf_layout 应回落到 grid，实际 %q", got.ShelfLayout)
	}
	if got.ShelfGridColumns != 6 {
		t.Fatalf("shelf_grid_columns 应夹到上限 6，实际 %d", got.ShelfGridColumns)
	}
	if got.ShelfSort != "recent" {
		t.Fatalf("非法 shelf_sort 应回落到 recent，实际 %q", got.ShelfSort)
	}
	// 布尔值不参与收敛，必须原样保留
	if !got.ShelfShowUnread || got.ShelfShowUpdateTime {
		t.Fatalf("布尔项被改动: unread=%v update=%v", got.ShelfShowUnread, got.ShelfShowUpdateTime)
	}
}

func TestReaderProfileIsPerUser(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	if _, code := putProfile(t, router, "u1", `{
		"theme_id":"preset3","night":true,"page_mode":"page","font_size":18,
		"line_height":1.5,"paragraph_spacing":4,"audio_speed":1,
		"audio_timer_minutes":0,"shelf_layout":"list","shelf_grid_columns":0,
		"shelf_sort":"name","shelf_show_unread":true,"shelf_show_update_time":true
	}`); code != http.StatusOK {
		t.Fatalf("u1 保存失败 status=%d", code)
	}

	// 另一个用户不应看到 u1 的偏好，也不应在 u1 之外建出记录
	other, code := getProfile(t, router, "u2")
	if code != http.StatusOK {
		t.Fatalf("u2 读取 status=%d", code)
	}
	if other != nil {
		t.Fatalf("u2 应返回 null，实际 %+v", other)
	}

	if _, code := putProfile(t, router, "u2", `{
		"theme_id":"preset4","night":false,"page_mode":"scroll","font_size":20,
		"line_height":1.8,"paragraph_spacing":8,"audio_speed":2,
		"audio_timer_minutes":60,"shelf_layout":"grid","shelf_grid_columns":3,
		"shelf_sort":"author","shelf_show_unread":false,"shelf_show_update_time":false
	}`); code != http.StatusOK {
		t.Fatalf("u2 保存失败 status=%d", code)
	}

	// u1 的值不能被 u2 覆盖
	back, code := getProfile(t, router, "u1")
	if code != http.StatusOK || back == nil {
		t.Fatalf("u1 读回失败 status=%d", code)
	}
	if back.ThemeID != "preset3" || back.ShelfLayout != "list" || back.ShelfSort != "name" || !back.Night {
		t.Fatalf("u1 的偏好被 u2 覆盖: %+v", *back)
	}
}

func TestReaderProfileSaveTwiceKeepsOneRow(t *testing.T) {
	svc := newReaderHandlerContainer(t)
	router := profileRouterForTest(t, svc)

	first := `{"theme_id":"preset1","page_mode":"page","font_size":20,"line_height":1.8,
		"paragraph_spacing":8,"audio_speed":1,"shelf_layout":"grid","shelf_sort":"recent"}`
	if _, code := putProfile(t, router, "u1", first); code != http.StatusOK {
		t.Fatalf("首次保存 status=%d", code)
	}
	second := `{"theme_id":"preset5","page_mode":"scroll","font_size":22,"line_height":2.0,
		"paragraph_spacing":10,"audio_speed":1.25,"shelf_layout":"compact","shelf_sort":"mixed"}`
	if _, code := putProfile(t, router, "u1", second); code != http.StatusOK {
		t.Fatalf("二次保存 status=%d", code)
	}

	got, _ := getProfile(t, router, "u1")
	if got == nil || got.ThemeID != "preset5" || got.ShelfSort != "mixed" {
		t.Fatalf("二次保存未覆盖旧值: %+v", got)
	}

	// 一个用户只应有一条记录（唯一索引 + upsert）
	var count int64
	if err := svc.Repo.DB.Model(&model.ReaderProfile{}).
		Where("user_id = ?", "u1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("u1 的记录数 = %d，期望 1", count)
	}
}
