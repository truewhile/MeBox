// Package repository 实现基于 GORM 的数据访问层。
// 每个方法接受 context.Context 以便后续插入取消/追踪。
//
// Repository 故意保持精简：它们只负责持久化数据，不处理业务逻辑。
// 业务逻辑位于 internal/service。
package repository

import "gorm.io/gorm"

// Container 是所有 repositories 的注册表，注入到 services 中。
type Container struct {
	DB             *gorm.DB
	User           *UserRepository
	Library        *LibraryRepository
	Media          *MediaRepository
	Series         *SeriesRepository
	History        *HistoryRepository
	MediaSegment   *MediaSegmentRepository
	Favorite       *FavoriteRepository
	Playlist       *PlaylistRepository
	Setting        *SettingRepository
	Log            *AccessLogRepository
	Permission     *PermissionRepository
	RefreshToken   *RefreshTokenRepository
	ApiConfig      *ApiConfigRepository
	PlayProfile    *PlayProfileRepository
	RegCode        *RegistrationCodeRepository
	SignIn         *SignInRepository
	UserDevice     *UserDeviceRepository
	StrmAccount    *StrmAccountRepository
	StrmSyncPath   *StrmSyncPathRepository
	StrmSyncRecord *StrmSyncRecordRepository
	StrmDownload   *StrmDownloadTaskRepository
	StrmUpload     *StrmUploadTaskRepository
	StrmDirCache   *StrmDirCacheRepository
	ScrapeTask     *ScrapeTaskRepository
	EmbyMount      *EmbyMountRepository
}

// New 将每个 repository 连接到单个 *gorm.DB。
func New(db *gorm.DB) *Container {
	return &Container{
		DB:             db,
		User:           &UserRepository{db: db},
		Library:        &LibraryRepository{db: db},
		Media:          &MediaRepository{db: db},
		Series:         &SeriesRepository{db: db},
		History:        &HistoryRepository{db: db},
		MediaSegment:   &MediaSegmentRepository{db: db},
		Favorite:       &FavoriteRepository{db: db},
		Playlist:       &PlaylistRepository{db: db},
		Setting:        &SettingRepository{db: db},
		Log:            &AccessLogRepository{db: db},
		Permission:     &PermissionRepository{db: db},
		RefreshToken:   &RefreshTokenRepository{db: db},
		ApiConfig:      &ApiConfigRepository{db: db},
		PlayProfile:    &PlayProfileRepository{db: db},
		RegCode:        &RegistrationCodeRepository{db: db},
		SignIn:         &SignInRepository{db: db},
		UserDevice:     &UserDeviceRepository{db: db},
		StrmAccount:    &StrmAccountRepository{db: db},
		StrmSyncPath:   &StrmSyncPathRepository{db: db},
		StrmSyncRecord: &StrmSyncRecordRepository{db: db},
		StrmDownload:   &StrmDownloadTaskRepository{db: db},
		StrmUpload:     &StrmUploadTaskRepository{db: db},
		StrmDirCache:   &StrmDirCacheRepository{db: db},
		ScrapeTask:     &ScrapeTaskRepository{db: db},
		EmbyMount:      &EmbyMountRepository{db: db},
	}
}
