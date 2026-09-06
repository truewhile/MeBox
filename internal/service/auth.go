// Package service — authentication / user management.
package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/truewhile/MeBox/internal/config"
	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

type temporaryPasswordEntry struct {
	userID    string
	username  string
	expiresAt time.Time
}

// AuthService handles registration, login, and JWT issuance.
type AuthService struct {
	cfg           *config.Config
	log           *zap.Logger
	repo          *repository.Container
	tokenSvc      *TokenService
	permissionSvc *PermissionService

	tempPassMu    sync.RWMutex
	tempPasswords map[string]temporaryPasswordEntry
}

// NewAuthService is the constructor.
func NewAuthService(cfg *config.Config, log *zap.Logger, repo *repository.Container, tokenSvc *TokenService, permissionSvc *PermissionService) *AuthService {
	return &AuthService{
		cfg:           cfg,
		log:           log,
		repo:          repo,
		tokenSvc:      tokenSvc,
		permissionSvc: permissionSvc,
		tempPasswords: make(map[string]temporaryPasswordEntry),
	}
}

// Common service-level errors.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUsernameTaken      = errors.New("username already taken")
	ErrUserInactive       = errors.New("user account is inactive")
	ErrUserLimitReached   = errors.New("user limit reached")
	ErrUserExpired        = errors.New("user account has expired")
)

// MaxUsers is kept for tests that seed up to the default cap.
const MaxUsers = DefaultMaxUsers

// SeedAdmin makes sure at least one admin user exists. It mirrors the
// legacy default behaviour: if no admin row is found we create
// `admin / admin123` (overridable through ADMIN_INITIAL_PASSWORD) and warn.
func (s *AuthService) SeedAdmin(ctx context.Context) error {
	n, err := s.repo.User.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pwd := os.Getenv("ADMIN_INITIAL_PASSWORD")
	if pwd == "" {
		pwd = "admin123"
	}
	hash, err := hashPassword(pwd)
	if err != nil {
		return err
	}
	user := &model.User{
		Username:           "admin",
		PasswordHash:       hash,
		Role:               "admin",
		Tier:               "plus",
		HideAdult:          true,
		ForcePasswordReset: pwd == "admin123",
	}
	if err := s.repo.User.Create(ctx, user); err != nil {
		return err
	}
	// 确保管理员有权限记录
	_, _ = s.permissionSvc.EnsureForUser(ctx, user.ID)
	s.log.Warn("default admin created — change the password after first login",
		zap.String("username", "admin"),
		zap.String("password_source", "ADMIN_INITIAL_PASSWORD or admin123"),
	)
	return nil
}

// Register creates a new user. The first registered user is auto-promoted to
// admin to support fresh installs that did not run SeedAdmin.
func (s *AuthService) Register(ctx context.Context, username, password string) (*model.User, *TokenPair, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, nil, fmt.Errorf("username and password required")
	}
	if existing, err := s.repo.User.FindByUsername(ctx, username); err != nil {
		return nil, nil, err
	} else if existing != nil {
		return nil, nil, ErrUsernameTaken
	}
	if err := s.repo.User.ReleaseDeletedUsername(ctx, username); err != nil {
		return nil, nil, err
	}
	limit, err := LoadMaxUsers(ctx, s.repo)
	if err != nil {
		return nil, nil, err
	}
	if n, err := s.repo.User.Count(ctx); err != nil {
		return nil, nil, err
	} else if n >= int64(limit) {
		return nil, nil, ErrUserLimitReached
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, nil, err
	}
	role := "user"
	if n, err := s.repo.User.CountAdmins(ctx); err == nil && n == 0 {
		role = "admin"
	}
	u := &model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Tier:         "free",
		HideAdult:    true,
	}
	if err := s.repo.User.Create(ctx, u); err != nil {
		return nil, nil, err
	}
	// 自动为新用户创建默认权限
	_, _ = s.permissionSvc.EnsureForUser(ctx, u.ID)
	// 签发令牌对
	tokens, err := s.tokenSvc.IssuePair(ctx, u.ID, u.Role, u.Tier)
	if err != nil {
		return u, nil, nil // 用户已创建，令牌签发失败不影响注册成功
	}
	return u, tokens, nil
}

// LoginResponse 登录响应结构。
type LoginResponse struct {
	User   *model.User `json:"user"`
	Tokens *TokenPair  `json:"tokens"`
}

// Login validates credentials and returns the user + a fresh JWT token pair.
func (s *AuthService) Login(ctx context.Context, username, password string) (*LoginResponse, error) {
	u, err := s.repo.User.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, ErrInvalidCredentials
	}
	// 检查用户是否激活
	if !u.IsActive {
		return nil, ErrUserInactive
	}
	// 账号到期则停用登录，直到管理员或兑换码续期。
	if u.ExpiredAt != nil && time.Now().After(*u.ExpiredAt) {
		return nil, ErrUserExpired
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	// 签发令牌对
	tokens, err := s.tokenSvc.IssuePairBestEffort(ctx, u.ID, u.Role, u.Tier)
	if err != nil {
		return nil, err
	}
	s.touchLoginBestEffort(u.ID)
	return &LoginResponse{User: u, Tokens: tokens}, nil
}

func (s *AuthService) touchLoginBestEffort(userID string) {
	if s == nil || s.repo == nil || s.repo.User == nil || strings.TrimSpace(userID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		helper.Run(s.log, "auth.touchLogin", func() {
			if err := s.repo.User.TouchLogin(ctx, userID); err != nil && s.log != nil {
				s.log.Debug("touch login delayed", zap.String("user_id", userID), zap.Error(err))
			}
		})
	}()
}

// ChangePassword updates the user password if the old one matches.
func (s *AuthService) ChangePassword(ctx context.Context, userID, oldPwd, newPwd string) error {
	if strings.TrimSpace(newPwd) == "" || len(newPwd) < 6 {
		return errors.New("new password must be at least 6 characters")
	}
	u, err := s.repo.User.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPwd)); err != nil {
		return ErrInvalidCredentials
	}
	hash, err := hashPassword(newPwd)
	if err != nil {
		return err
	}
	return s.repo.User.UpdatePassword(ctx, userID, hash)
}

// ResetPassword lets an administrator set a new password without knowing the
// user's old password.
func (s *AuthService) ResetPassword(ctx context.Context, userID, newPwd string) error {
	if strings.TrimSpace(userID) == "" {
		return errors.New("missing user id")
	}
	if strings.TrimSpace(newPwd) == "" || len(newPwd) < 6 {
		return errors.New("new password must be at least 6 characters")
	}
	u, err := s.repo.User.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil {
		return errors.New("user not found")
	}
	hash, err := hashPassword(newPwd)
	if err != nil {
		return err
	}
	return s.repo.User.UpdatePassword(ctx, userID, hash)
}

// VerifyPassword checks a user's current password without mutating account
// state. It is used for sensitive self-service actions such as hiding adult
// libraries or deleting play profiles.
func (s *AuthService) VerifyPassword(ctx context.Context, userID, password string) error {
	u, err := s.repo.User.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil || strings.TrimSpace(password) == "" {
		return ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

func hashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

const temporaryPasswordTTL = 5 * time.Minute

// CreateTemporaryPassword 为指定用户生成一个 6 位数字的临时登录密码（有效期 5 分钟），
// 供 Emby 电视端/客户端进行无键盘或快速输入登录。
func (s *AuthService) CreateTemporaryPassword(ctx context.Context, userID string) (string, int, error) {
	if s == nil || s.repo == nil {
		return "", 0, errors.New("auth service unavailable")
	}
	user, err := s.repo.User.FindByID(ctx, userID)
	if err != nil || user == nil {
		return "", 0, ErrInvalidCredentials
	}
	if !user.IsActive {
		return "", 0, ErrUserInactive
	}
	if user.ExpiredAt != nil && time.Now().After(*user.ExpiredAt) {
		return "", 0, ErrUserExpired
	}

	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return "", 0, err
	}
	code := fmt.Sprintf("%06d", n.Int64()+100000)

	s.tempPassMu.Lock()
	defer s.tempPassMu.Unlock()
	now := time.Now()
	for k, v := range s.tempPasswords {
		if now.After(v.expiresAt) {
			delete(s.tempPasswords, k)
		}
	}
	s.tempPasswords[code] = temporaryPasswordEntry{
		userID:    user.ID,
		username:  user.Username,
		expiresAt: now.Add(temporaryPasswordTTL),
	}

	return code, int(temporaryPasswordTTL.Seconds()), nil
}

// VerifyAndConsumeTemporaryPassword 校验并消费临时登录密码（阅后即焚）。
func (s *AuthService) VerifyAndConsumeTemporaryPassword(ctx context.Context, username, code string) (*model.User, bool) {
	if s == nil || s.repo == nil {
		return nil, false
	}
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return nil, false
	}
	s.tempPassMu.Lock()
	entry, ok := s.tempPasswords[code]
	if ok {
		delete(s.tempPasswords, code)
	}
	s.tempPassMu.Unlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}

	if strings.TrimSpace(username) != "" && !strings.EqualFold(strings.TrimSpace(username), entry.username) {
		return nil, false
	}

	user, err := s.repo.User.FindByID(ctx, entry.userID)
	if err != nil || user == nil || !user.IsActive {
		return nil, false
	}
	if user.ExpiredAt != nil && time.Now().After(*user.ExpiredAt) {
		return nil, false
	}
	return user, true
}

// LoginWithTemporaryPassword 尝试使用 6 位数字临时登录密码 (OTP) 进行登录。
func (s *AuthService) LoginWithTemporaryPassword(ctx context.Context, username, code string) (*LoginResponse, error) {
	user, ok := s.VerifyAndConsumeTemporaryPassword(ctx, username, code)
	if !ok || user == nil {
		return nil, ErrInvalidCredentials
	}
	if s.tokenSvc == nil {
		return nil, errors.New("token service unavailable")
	}
	tokens, err := s.tokenSvc.IssuePair(ctx, user.ID, user.Role, user.Tier)
	if err != nil {
		return nil, err
	}
	return &LoginResponse{
		User:   user,
		Tokens: tokens,
	}, nil
}
