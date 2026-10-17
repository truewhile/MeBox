package service

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

const loginRefreshTokenStoreTimeout = 750 * time.Millisecond

type pendingRefreshToken struct {
	UserID    string
	SessionID string
	ExpiresAt time.Time
}

func (s *TokenService) storeRefreshTokenBestEffort(userID, tokenHash, sessionID string, expiresAt time.Time) {
	if !s.trackDelayedStore(userID, tokenHash, sessionID, expiresAt) {
		return
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := helper.Recover(s.log, "token.storeRefreshToken", func() error {
			return s.storeRefreshToken(ctx, &model.RefreshToken{
				UserID:        userID,
				TokenHash:     tokenHash,
				AuthSessionID: sessionID,
				ExpiresAt:     expiresAt,
			})
		})
		done <- err
	}()
	select {
	case err := <-done:
		s.finishBestEffortRefreshTokenStore(userID, tokenHash, sessionID, expiresAt, err)
	case <-time.After(loginRefreshTokenStoreTimeout):
		if s.log != nil {
			s.log.Warn("refresh token store delayed; login will continue",
				zap.String("user_id", userID),
				zap.Error(context.DeadlineExceeded))
		}
		go func() {
			err := <-done
			s.finishBestEffortRefreshTokenStore(userID, tokenHash, sessionID, expiresAt, err)
		}()
	}
}

func (s *TokenService) finishBestEffortRefreshTokenStore(userID, tokenHash, sessionID string, expiresAt time.Time, err error) {
	if err == nil {
		s.untrackDelayedStore(userID, tokenHash)
		return
	}
	if repository.IsSQLiteBusyError(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		if s.log != nil {
			s.log.Warn("refresh token store delayed; login will continue",
				zap.String("user_id", userID),
				zap.Error(err))
		}
		s.storeRefreshTokenEventually(userID, tokenHash, sessionID, expiresAt)
		return
	}
	s.untrackDelayedStore(userID, tokenHash)
	if s.log != nil {
		s.log.Warn("refresh token delayed store failed permanently", zap.String("user_id", userID), zap.Error(err))
	}
}

func (s *TokenService) storeRefreshTokenEventually(userID, tokenHash, sessionID string, expiresAt time.Time) {
	defer s.untrackDelayedStore(userID, tokenHash)
	delay := time.Second
	for attempt := 1; attempt <= 8; attempt++ {
		timer := time.NewTimer(delay)
		<-timer.C
		// ?????????????/???? pending ?????
		// ?????????????????????????
		if _, stillPending := s.pendingDelayedStore(tokenHash); !stillPending {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.storeRefreshToken(ctx, &model.RefreshToken{
			UserID:        userID,
			TokenHash:     tokenHash,
			AuthSessionID: sessionID,
			ExpiresAt:     expiresAt,
		})
		cancel()
		if err == nil {
			return
		}
		if !repository.IsSQLiteBusyError(err) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			if s.log != nil {
				s.log.Warn("refresh token delayed store failed permanently", zap.String("user_id", userID), zap.Error(err))
			}
			return
		}
		if s.log != nil && (attempt == 1 || attempt == 4 || attempt == 8) {
			s.log.Warn("refresh token delayed store still waiting",
				zap.String("user_id", userID),
				zap.Int("attempt", attempt),
				zap.Error(err))
		}
		if delay < 60*time.Second {
			delay *= 2
		}
	}
	if s.log != nil {
		s.log.Warn("refresh token delayed store gave up", zap.String("user_id", userID))
	}
}

func (s *TokenService) trackDelayedStore(userID, tokenHash, sessionID string, expiresAt time.Time) bool {
	if s == nil {
		return false
	}
	s.delayedStoreMu.Lock()
	defer s.delayedStoreMu.Unlock()
	if s.delayedStores == nil {
		s.delayedStores = make(map[string]pendingRefreshToken)
	}
	if _, ok := s.delayedStores[tokenHash]; ok {
		return false
	}
	s.delayedStores[tokenHash] = pendingRefreshToken{UserID: userID, SessionID: sessionID, ExpiresAt: expiresAt}
	return true
}

func (s *TokenService) untrackDelayedStore(userID, tokenHash string) {
	if s == nil {
		return
	}
	s.delayedStoreMu.Lock()
	delete(s.delayedStores, tokenHash)
	s.delayedStoreMu.Unlock()
}

// pendingDelayedStore ??????? refresh token ?????????
func (s *TokenService) pendingDelayedStore(tokenHash string) (pendingRefreshToken, bool) {
	if s == nil {
		return pendingRefreshToken{}, false
	}
	s.delayedStoreMu.Lock()
	defer s.delayedStoreMu.Unlock()
	pending, ok := s.delayedStores[tokenHash]
	return pending, ok
}
