package service

import (
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newRotationTestService(t *testing.T) (*TokenService, *repository.Container, *gorm.DB) {
	t.Helper()
	db := newServiceTestDB(t, &model.User{}, &model.RefreshToken{}, &model.Setting{})
	repos := repository.New(db)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret"
	return NewTokenService(cfg, zap.NewNop(), repos), repos, db
}

func seedRotationUser(t *testing.T, repos *repository.Container) *model.User {
	t.Helper()
	u := &model.User{Username: "race", PasswordHash: "x", Role: "user", Tier: "free", IsActive: true}
	if err := repos.User.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

// TestConcurrentRefreshSharesSingleRotation 复现真实客户端的并发刷新：
// WebSocket 重连与 401 拦截器、多个标签页会同时用同一个 refresh token 刷新。
// 之前第二个请求必然拿到 401 revoked，前端据此清空会话（部署后被迫重新登录）。
// 现在并发请求共享同一次轮换，全部成功且拿到同一对令牌。
func TestConcurrentRefreshSharesSingleRotation(t *testing.T) {
	svc, repos, db := newRotationTestService(t)
	u := seedRotationUser(t, repos)

	pair, err := svc.IssuePair(t.Context(), u.ID, u.Role, u.Tier)
	if err != nil {
		t.Fatal(err)
	}

	const concurrency = 8
	pairs := make([]*TokenPair, concurrency)
	errs := make([]error, concurrency)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			pairs[idx], errs[idx] = svc.Refresh(t.Context(), pair.RefreshToken)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < concurrency; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent refresh %d failed: %v", i, errs[i])
		}
		if pairs[i].AccessToken != pairs[0].AccessToken || pairs[i].RefreshToken != pairs[0].RefreshToken {
			t.Fatalf("concurrent refresh %d returned a different pair", i)
		}
	}

	// 只应该产生一个新的活跃 refresh token，而不是每个请求各轮换一次。
	var active int64
	if err := db.Model(&model.RefreshToken{}).
		Where("user_id = ? AND revoked = ?", u.ID, false).
		Count(&active).Error; err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active refresh tokens = %d, want 1", active)
	}
}

// TestRefreshReuseAfterRotationReturnsSamePair 验证轮换完成后（并发窗口已
// 结束）重复提交同一个旧令牌仍然是幂等的，客户端重试不会掉登录。
func TestRefreshReuseAfterRotationReturnsSamePair(t *testing.T) {
	svc, repos, _ := newRotationTestService(t)
	u := seedRotationUser(t, repos)

	pair, err := svc.IssuePair(t.Context(), u.ID, u.Role, u.Tier)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.Refresh(t.Context(), pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := svc.Refresh(t.Context(), pair.RefreshToken)
	if err != nil {
		t.Fatalf("reuse within grace window: %v", err)
	}
	if reused.RefreshToken != first.RefreshToken || reused.AccessToken != first.AccessToken {
		t.Fatal("reuse must return the pair issued by the first rotation")
	}
	// 复用的是新令牌，仍然可以继续轮换。
	if _, err := svc.Refresh(t.Context(), reused.RefreshToken); err != nil {
		t.Fatalf("refreshed token must stay usable: %v", err)
	}
}

// TestRefreshRotationReuseIsPerToken 验证复用表不会跨令牌串号：
// 另一个令牌（例如被设备上限淘汰的那个）不会被误判为可复用。
func TestRefreshRotationReuseIsPerToken(t *testing.T) {
	svc, repos, _ := newRotationTestService(t)
	u := seedRotationUser(t, repos)

	rotated, err := svc.IssuePair(t.Context(), u.ID, u.Role, u.Tier)
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.IssuePair(t.Context(), u.ID, u.Role, u.Tier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(t.Context(), rotated.RefreshToken); err != nil {
		t.Fatal(err)
	}

	// 主动撤销另一个令牌（模拟登出/被踢下线），它不应享有复用宽限。
	if err := repos.RefreshToken.Revoke(t.Context(), repository.HashToken(other.RefreshToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(t.Context(), other.RefreshToken); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("revoked token error = %v, want ErrTokenRevoked", err)
	}
}

// TestRefreshGraceWindowExpires 验证宽限期是有限的：超过之后旧令牌
// 依旧被拒绝，管理员重置密码/踢下线等撤销语义不受影响。
func TestRefreshGraceWindowExpires(t *testing.T) {
	svc, repos, _ := newRotationTestService(t)
	u := seedRotationUser(t, repos)

	pair, err := svc.IssuePair(t.Context(), u.ID, u.Role, u.Tier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(t.Context(), pair.RefreshToken); err != nil {
		t.Fatal(err)
	}

	base := time.Now()
	svc.now = func() time.Time { return base.Add(refreshTokenReuseGrace + time.Second) }
	if _, err := svc.Refresh(t.Context(), pair.RefreshToken); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("error after grace window = %v, want ErrTokenRevoked", err)
	}
}

// TestRevokeAllDropsRotationReuse 验证登出/被踢下线立即生效：
// 复用宽限期不能成为旧令牌继续换新的后门。
func TestRevokeAllDropsRotationReuse(t *testing.T) {
	svc, repos, _ := newRotationTestService(t)
	u := seedRotationUser(t, repos)

	pair, err := svc.IssuePair(t.Context(), u.ID, u.Role, u.Tier)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := svc.Refresh(t.Context(), pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAll(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Refresh(t.Context(), pair.RefreshToken); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("old token error = %v, want ErrTokenRevoked", err)
	}
	if _, err := svc.Refresh(t.Context(), rotated.RefreshToken); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("rotated token error = %v, want ErrTokenRevoked", err)
	}
}
