// Package service — audit log helper.
//
// AuditService writes structured AccessLog rows for sensitive actions
// (login, library CRUD, scrape / scan triggers, download enqueue, etc).
// It deliberately swallows write errors so audit failures never bubble
// up to the caller.
package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/helper"
	"github.com/truewhile/MeBox/internal/model"
	"github.com/truewhile/MeBox/internal/repository"
)

// AuditService is the only sanctioned writer for the access_logs table.
type AuditService struct {
	log  *zap.Logger
	repo *repository.Container
}

// NewAuditService is the constructor.
func NewAuditService(log *zap.Logger, repo *repository.Container) *AuditService {
	return &AuditService{log: log, repo: repo}
}

// Record persists one audit row.
func (a *AuditService) Record(ctx context.Context, userID, action, target, ip, detail string) {
	row := &model.AccessLog{
		UserID: userID,
		Action: action,
		Target: target,
		IP:     ip,
		Detail: detail,
	}
	if err := a.repo.Log.Create(ctx, row); err != nil {
		a.log.Debug("audit write failed", zap.Error(err))
	}
}

// RecordBestEffort writes an audit row off the request path. Login must not be
// held open by SQLite write pressure from scans or background maintenance.
func (a *AuditService) RecordBestEffort(userID, action, target, ip, detail string) {
	if a == nil || a.repo == nil || a.repo.Log == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		helper.Run(a.log, "audit.record", func() {
			a.Record(ctx, userID, action, target, ip, detail)
		})
	}()
}
