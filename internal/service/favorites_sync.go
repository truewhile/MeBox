package service

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// SyncUserFavorite keeps favourite state in the local favourites table, keyed
// by MeBox user_id. Remote Emby mounts share one upstream account, so favourite
// state is intentionally not proxied upstream.
func SyncUserFavorite(ctx context.Context, repo *repository.Container, _ *EmbyRemoteService, userID, mediaID string, favorite bool) error {
	if repo == nil || userID == "" || mediaID == "" {
		return errors.New("missing favourite sync inputs")
	}
	return setLocalFavorite(ctx, repo, userID, mediaID, favorite)
}

// IsUserFavorite reports whether the user has favourited mediaID locally.
func IsUserFavorite(ctx context.Context, repo *repository.Container, userID, mediaID string) (bool, error) {
	if repo == nil || userID == "" || mediaID == "" {
		return false, nil
	}
	var count int64
	err := repo.DB.WithContext(ctx).Model(&model.Favorite{}).
		Where("user_id = ? AND media_id = ?", userID, mediaID).
		Count(&count).Error
	return count > 0, err
}

func setLocalFavorite(ctx context.Context, repo *repository.Container, userID, mediaID string, favorite bool) error {
	if favorite {
		var existing model.Favorite
		err := repo.DB.WithContext(ctx).
			Where("user_id = ? AND media_id = ?", userID, mediaID).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return repo.DB.WithContext(ctx).Create(&model.Favorite{
				UserID:  userID,
				MediaID: mediaID,
			}).Error
		}
		return err
	}
	return repo.DB.WithContext(ctx).
		Where("user_id = ? AND media_id = ?", userID, mediaID).
		Delete(&model.Favorite{}).Error
}
