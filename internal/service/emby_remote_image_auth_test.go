package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestRemoteEmbyImageTokenForHostUnknownHost(t *testing.T) {
	svc, _, _ := newImageTagTestService(t)

	if token, ok := svc.RemoteEmbyImageTokenForHost(t.Context(), "nope.example"); ok || token != "" {
		t.Fatalf("got (%q,%v), want no token for a host not owned by any account", token, ok)
	}
}

func TestRemoteEmbyImageTokenForHostMatchesConfiguredLine(t *testing.T) {
	svc, _, _ := newImageTagTestService(t)

	token, ok := svc.RemoteEmbyImageTokenForHost(t.Context(), "emby.test")
	if !ok || token != "fake-token" {
		t.Fatalf("got (%q,%v), want the account token for the configured line host", token, ok)
	}
}

func TestRefreshRemoteEmbyImageTokenForHostRequiresCredentials(t *testing.T) {
	svc, _, _ := newImageTagTestService(t)

	// The account carries only an api_key — there is nothing to re-authenticate with.
	if _, err := svc.RefreshRemoteEmbyImageTokenForHost(t.Context(), "emby.test"); err == nil {
		t.Fatal("expected an error when the account has no username/password to re-authenticate with")
	}
}

// TestRefreshRemoteEmbyImageTokenForHostRelogins 覆盖「token 被撤销后自动恢复」：
// 用用户名/密码重新登录拿到新 token，并持久化，后续请求不再重复登录。
func TestRefreshRemoteEmbyImageTokenForHostRelogins(t *testing.T) {
	const fresh = "fresh-token"
	logins := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Users/AuthenticateByName" {
			http.NotFound(w, r)
			return
		}
		logins++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"AccessToken":"` + fresh + `","User":{"Id":"remote-user"}}`))
	}))
	defer upstream.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-refresh"},
		Name:     "refresh-emby",
		Provider: model.StrmProviderEmbyRemote,
		Enabled:  true,
		Config:   `{"url":"` + upstream.URL + `","api_key":"stale-token","username":"u","password":"p"}`,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatalf("create account: %v", err)
	}

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()

	if token, ok := svc.RemoteEmbyImageTokenForHost(t.Context(), host); !ok || token != "stale-token" {
		t.Fatalf("token before refresh = (%q,%v), want the stale stored token", token, ok)
	}

	token, err := svc.RefreshRemoteEmbyImageTokenForHost(t.Context(), host)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if token != fresh {
		t.Fatalf("refreshed token = %q, want %q", token, fresh)
	}
	if logins != 1 {
		t.Fatalf("logins = %d, want 1", logins)
	}

	// 新 token 必须落库，否则每个请求都要重新登录一次。
	if token, ok := svc.RemoteEmbyImageTokenForHost(t.Context(), host); !ok || token != fresh {
		t.Fatalf("token after refresh = (%q,%v), want the persisted %q", token, ok, fresh)
	}
	if logins != 1 {
		t.Fatalf("logins = %d, want 1 after the refresh was persisted", logins)
	}
}
