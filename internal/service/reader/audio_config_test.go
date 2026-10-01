package reader

import (
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

// TestSaveAudioConfigPersistsCredits 听书跳过片头/片尾设置要落库，且只能改自己的书。
func TestSaveAudioConfigPersistsCredits(t *testing.T) {
	svc, repos := newLoginTestService(t)
	ctx := t.Context()

	book := &model.ReaderBook{UserID: "u1", Name: "宠魅", Type: 1}
	if err := repos.Reader.CreateBook(ctx, book); err != nil {
		t.Fatal(err)
	}

	if err := svc.SaveAudioConfig(ctx, "u1", book.ID, 30, 15); err != nil {
		t.Fatalf("保存听书设置失败: %v", err)
	}
	got, err := svc.GetBook(ctx, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenCredits != 30 || got.CloseCredits != 15 {
		t.Fatalf("片头/片尾 = %d/%d，期望 30/15", got.OpenCredits, got.CloseCredits)
	}

	// 0 是合法值（不跳过），必须能写回
	if err := svc.SaveAudioConfig(ctx, "u1", book.ID, 0, 0); err != nil {
		t.Fatalf("清零失败: %v", err)
	}
	if got, _ = svc.GetBook(ctx, book.ID); got.OpenCredits != 0 || got.CloseCredits != 0 {
		t.Fatalf("清零后 = %d/%d，期望 0/0", got.OpenCredits, got.CloseCredits)
	}

	if err := svc.SaveAudioConfig(ctx, "u1", book.ID, -1, 0); err == nil {
		t.Fatal("负数应被拒绝")
	}
	if err := svc.SaveAudioConfig(ctx, "other", book.ID, 10, 10); err == nil {
		t.Fatal("他人书架应被拒绝")
	}
}
