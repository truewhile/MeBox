package service

import (
	"path/filepath"
	"testing"

	"github.com/truewhile/MeBox/internal/model"
)

func TestMediaDisplayLibraryResolverUsesMostSpecificPath(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "media")
	nestedPath := filepath.Join(parentPath, "anime")
	parent := model.Library{
		Base:    model.Base{ID: "parent"},
		Name:    "媒体",
		Path:    parentPath,
		Enabled: true,
	}
	nested := model.Library{
		Base:    model.Base{ID: "nested"},
		Name:    "动漫",
		Path:    nestedPath,
		Enabled: true,
	}

	resolver := newMediaDisplayLibraryResolver(t.Context(), nil, []model.Library{parent, nested})
	got, ok := resolver.DisplayLibraryForMedia(model.Media{
		LibraryID: parent.ID,
		Path:      filepath.Join(nestedPath, "Show", "S01E01.mkv"),
	})
	if !ok || got.ID != nested.ID {
		t.Fatalf("display library = %q, %t; want nested library", got.ID, ok)
	}

	got, ok = resolver.DisplayLibraryForMedia(model.Media{
		LibraryID: parent.ID,
		Path:      filepath.Join(parentPath, "Movie", "Movie.mkv"),
	})
	if !ok || got.ID != parent.ID {
		t.Fatalf("display library = %q, %t; want parent library", got.ID, ok)
	}
}
