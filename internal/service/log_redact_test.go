package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRedactSensitiveURL(t *testing.T) {
	raw := "https://user:secret@media.example/emby/Items/1/Images/primary?api_key=abc123&X-Emby-Token=xyz&X-Amz-Signature=sig123&quality=90"
	got := redactSensitiveURL(raw)
	for _, secret := range []string{"secret", "abc123", "xyz", "sig123"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted URL still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "quality=90") {
		t.Fatalf("non-sensitive query value was removed: %s", got)
	}
}

func TestRedactSensitiveTextCoversJSONAndHeaders(t *testing.T) {
	raw := `{"AccessToken":"json-token","quality":"90"} Authorization: Bearer bearer-token; X-Emby-Token=header-token`
	got := redactSensitiveText(raw)
	for _, secret := range []string{"json-token", "bearer-token", "header-token"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"quality":"90"`) {
		t.Fatalf("non-sensitive JSON value was removed: %s", got)
	}
}

func TestRedactSensitiveErrorPreservesWrapping(t *testing.T) {
	base := errors.New(`Get "https://media.example/item?token=secret&quality=90": context canceled`)
	err := redactSensitiveError(context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("redacted error lost context.Canceled")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("redacted error leaked token: %s", err.Error())
	}
	redactedBase := redactSensitiveError(base)
	if strings.Contains(redactedBase.Error(), "secret") {
		t.Fatalf("redacted base error leaked token: %s", redactedBase.Error())
	}
	if !errors.Is(redactedBase, base) {
		t.Fatal("redacted error lost original error identity")
	}
}
