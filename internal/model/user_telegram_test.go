package model

import (
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// TelegramChatID 是 Telegram 通知的绑定目标：Size 必须容得下真实 chat id
// （群/频道 id 为负数且位数更长），因此下限设为 64。
func TestUserTelegramChatIDFieldSize(t *testing.T) {
	parsed, err := schema.Parse(&User{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	field := parsed.LookUpField("TelegramChatID")
	if field == nil {
		t.Fatal("TelegramChatID field not found")
	}
	if field.Size < 64 {
		t.Fatalf("TelegramChatID size = %d, want at least 64", field.Size)
	}
}
