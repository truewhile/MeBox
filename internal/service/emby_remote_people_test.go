package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

func TestRemoteItemRequestsPeopleAndRewritesPersonIDs(t *testing.T) {
	var requestedFields string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/Users/user-1/Items/item-1") {
			http.NotFound(w, r)
			return
		}
		requestedFields = r.URL.Query().Get("Fields")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":   "item-1",
			"Name": "测试电影",
			"People": []map[string]any{
				{
					"Id":              "person-1",
					"Name":            "演员甲",
					"Type":            "Actor",
					"PrimaryImageTag": "person-tag-1",
				},
			},
		})
	}))
	defer server.Close()

	db := newServiceTestDB(t, &model.StrmAccount{}, &model.EmbyMount{})
	repos := repository.New(db)
	svc := NewEmbyRemoteService(&config.Config{}, zap.NewNop(), repos, NewCryptoService("", zap.NewNop()))
	rawConfig, _ := json.Marshal(map[string]string{
		"url":            server.URL,
		"token":          "fake-token",
		"remote_user_id": "user-1",
	})
	acct := &model.StrmAccount{
		Base:     model.Base{ID: "acct-people"},
		Provider: model.StrmProviderEmbyRemote,
		Config:   string(rawConfig),
		Enabled:  true,
	}
	if err := repos.StrmAccount.Create(t.Context(), acct); err != nil {
		t.Fatal(err)
	}
	mount := &model.EmbyMount{Base: model.Base{ID: "mount-people"}, AccountID: acct.ID}

	out, err := svc.RemoteItem(t.Context(), mount, acct, "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestedFields, "People") {
		t.Fatalf("RemoteItem Fields = %q, want People", requestedFields)
	}
	people, ok := out["People"].([]any)
	if !ok || len(people) != 1 {
		t.Fatalf("People = %#v, want one person", out["People"])
	}
	person := people[0].(map[string]any)
	if got := person["Id"]; got != EncodeEmbyRemoteID("mount-people", "person-1") {
		t.Fatalf("person Id = %v, want encoded remote id", got)
	}
	if person["Name"] != "演员甲" || person["PrimaryImageTag"] != "person-tag-1" {
		t.Fatalf("person display fields changed: %#v", person)
	}
	raw, ok := svc.ResolveRemotePersonImageURL(t.Context(), "演员甲", "Primary")
	if !ok || !strings.Contains(raw, "/Items/person-1/Images/primary") {
		t.Fatalf("resolved person image URL = %q, ok=%v", raw, ok)
	}
}
