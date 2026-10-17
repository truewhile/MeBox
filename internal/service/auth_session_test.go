package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func newAuthSessionTestEnv(t *testing.T) (*AuthSessionService, *TokenService, *repository.Container) {
	t.Helper()
	db := newServiceTestDB(t, &model.AuthSession{}, &model.RefreshToken{}, &model.User{}, &model.Setting{})
	repos := repository.New(db)
	sessions := NewAuthSessionService(zap.NewNop(), repos)
	cfg := &config.Config{}
	cfg.Secrets.JWTSecret = "test-secret"
	tokens := NewTokenService(cfg, zap.NewNop(), repos)
	tokens.SetSessionValidator(func(ctx context.Context, sessionID, userID string) error {
		_, err := sessions.Validate(ctx, sessionID, userID)
		return err
	})
	return sessions, tokens, repos
}

func seedSessionUser(t *testing.T, repos *repository.Container) *model.User {
	t.Helper()
	u := &model.User{Username: "mobile", PasswordHash: "x", Role: "user", Tier: "free", IsActive: true}
	if err := repos.User.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAuthSessionLifecycle(t *testing.T) {
	sessions, tokens, repos := newAuthSessionTestEnv(t)
	ctx := context.Background()
	user := seedSessionUser(t, repos)

	session, err := sessions.Create(ctx, CreateAuthSessionInput{
		UserID:     user.ID,
		DeviceID:   "device-abc",
		DeviceName: "Pixel",
		ClientName: "MeBox Android",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.ID == "" || session.ExpiresAt.IsZero() {
		t.Fatal("session missing id or expiry")
	}
	if _, err := sessions.Validate(ctx, session.ID, user.ID); err != nil {
		t.Fatalf("validate active session: %v", err)
	}
	if _, err := sessions.Validate(ctx, session.ID, "other-user"); !errors.Is(err, ErrAuthSessionRevoked) {
		t.Fatalf("cross-user validate err = %v, want ErrAuthSessionRevoked", err)
	}

	pair, err := tokens.IssuePairForSession(ctx, user.ID, user.Role, user.Tier, session.ID)
	if err != nil {
		t.Fatalf("issue pair: %v", err)
	}
	claims, err := tokens.ValidateAccessToken(pair.AccessToken)
	if err != nil {
		t.Fatalf("validate access token: %v", err)
	}
	if claims.Sid != session.ID {
		t.Fatalf("access token sid = %q, want %q", claims.Sid, session.ID)
	}
	rt, err := repos.RefreshToken.FindByHash(ctx, repository.HashToken(pair.RefreshToken))
	if err != nil || rt == nil {
		t.Fatalf("refresh token row = %v, err = %v", rt, err)
	}
	if rt.AuthSessionID != session.ID {
		t.Fatalf("refresh token session = %q, want %q", rt.AuthSessionID, session.ID)
	}

	if err := sessions.Revoke(ctx, session.ID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, err := sessions.Validate(ctx, session.ID, user.ID); !errors.Is(err, ErrAuthSessionRevoked) {
		t.Fatalf("validate revoked err = %v, want ErrAuthSessionRevoked", err)
	}
	rt, err = repos.RefreshToken.FindByHash(ctx, repository.HashToken(pair.RefreshToken))
	if err != nil || rt == nil {
		t.Fatalf("reload refresh token: %v", err)
	}
	if !rt.Revoked {
		t.Fatal("refresh token should be revoked with the session")
	}

	// 幂等：重复撤销不报错。
	if err := sessions.Revoke(ctx, session.ID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestRefreshRejectsRevokedSession(t *testing.T) {
	sessions, tokens, repos := newAuthSessionTestEnv(t)
	ctx := context.Background()
	user := seedSessionUser(t, repos)

	session, err := sessions.Create(ctx, CreateAuthSessionInput{UserID: user.ID, DeviceID: "device-1"})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tokens.IssuePairForSession(ctx, user.ID, user.Role, user.Tier, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.Revoke(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tokens.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrAuthSessionRevoked) {
		t.Fatalf("refresh revoked session err = %v, want ErrAuthSessionRevoked", err)
	}
}

func TestRefreshKeepsSessionAssociation(t *testing.T) {
	sessions, tokens, repos := newAuthSessionTestEnv(t)
	ctx := context.Background()
	user := seedSessionUser(t, repos)

	session, err := sessions.Create(ctx, CreateAuthSessionInput{UserID: user.ID, DeviceID: "device-2"})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tokens.IssuePairForSession(ctx, user.ID, user.Role, user.Tier, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := tokens.Refresh(ctx, pair.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	claims, err := tokens.ValidateAccessToken(rotated.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sid != session.ID {
		t.Fatalf("rotated access sid = %q, want %q", claims.Sid, session.ID)
	}
	// 刷新路径使用 best-effort 落库；轮换结果可能稍后才写入。
	var rt *model.RefreshToken
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rt, err = repos.RefreshToken.FindByHash(ctx, repository.HashToken(rotated.RefreshToken))
		if err == nil && rt != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || rt == nil {
		t.Fatalf("rotated refresh row: %v err=%v", rt, err)
	}
	if rt.AuthSessionID != session.ID {
		t.Fatalf("rotated refresh session = %q, want %q", rt.AuthSessionID, session.ID)
	}
}

func TestServerIdentityPersistsAcrossInstances(t *testing.T) {
	db := newServiceTestDB(t, &model.Setting{})
	repos := repository.New(db)
	ctx := context.Background()

	first := NewServerIdentityService(repos, "MeBox-v1.2.3")
	id1, err := first.ServerID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("empty server id")
	}

	// 独立服务实例（模拟重启）读到同一个持久身份。
	second := NewServerIdentityService(repos, "MeBox-v1.2.3")
	id2, err := second.ServerID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("server id changed across instances: %q != %q", id2, id1)
	}

	info, err := second.Info(ctx, ServerCapabilities{DeviceSessions: true})
	if err != nil {
		t.Fatal(err)
	}
	if info.ServerID != id1 || info.ServerVersion != "MeBox-v1.2.3" || info.APIVersion != "v1" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !info.Capabilities.DeviceSessions || info.Capabilities.PlaybackSessions {
		t.Fatalf("unexpected capabilities: %+v", info.Capabilities)
	}
}

func TestAuthSessionExpiry(t *testing.T) {
	sessions, _, repos := newAuthSessionTestEnv(t)
	user := seedSessionUser(t, repos)
	sessions.now = func() time.Time { return time.Now().Add(-40 * 24 * time.Hour) }
	session, err := sessions.Create(context.Background(), CreateAuthSessionInput{UserID: user.ID, DeviceID: "device-old"})
	if err != nil {
		t.Fatal(err)
	}
	sessions.now = time.Now
	if _, err := sessions.Validate(context.Background(), session.ID, user.ID); !errors.Is(err, ErrAuthSessionExpired) {
		t.Fatalf("expired validate err = %v, want ErrAuthSessionExpired", err)
	}
}
