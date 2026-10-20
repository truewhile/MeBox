package service

import (
	"context"
	"encoding/json"
	"html"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/truewhile/MeBox/internal/model"
)

const (
	TaskStatusRunning     = "running"
	TaskStatusCompleted   = "completed"
	TaskStatusFailed      = "failed"
	TaskStatusInterrupted = "interrupted"

	TaskKindOrganize = "organize"
	TaskKindScan     = "scan"
	TaskKindScrape   = "scrape"
	TaskKindUpdate   = "update"
)

// BackgroundTask is the compact, operator-facing shape shown on the live tasks
// page. It tracks long-running work that is not represented by a download or
// transcode job, such as organize → scan → scrape ingest flows.
type BackgroundTask struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	Status     string           `json:"status"`
	Stage      string           `json:"stage,omitempty"`
	SourcePath string           `json:"source_path,omitempty"`
	DestPath   string           `json:"dest_path,omitempty"`
	Message    string           `json:"message,omitempty"`
	Error      string           `json:"error,omitempty"`
	Details    []string         `json:"details,omitempty"`
	Metrics    map[string]int64 `json:"metrics,omitempty"`
	Items      []TaskItemRecord `json:"items,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
}

type TaskUpdate struct {
	Stage      string
	SourcePath string
	DestPath   string
	Message    string
	Details    []string
	Metrics    map[string]int64
	Items      []TaskItemRecord
}

type TaskSnapshot struct {
	Active []BackgroundTask `json:"active"`
	Recent []BackgroundTask `json:"recent"`
}

type TaskTrackerService struct {
	log *zap.Logger
	hub *Hub

	// failureNotifier 在任务以失败收尾时通知管理员（Telegram）。nil 时静默。
	failureNotifier func(ctx context.Context, text string)

	mu        sync.Mutex
	active    map[string]*BackgroundTask
	recent    []BackgroundTask
	maxRecent int
	now       func() time.Time
	db        *gorm.DB
}

// SetPersistence enables durable task history and marks tasks left running by
// a previous process as interrupted. Actual task execution is never resumed
// automatically; this record is an operator-visible recovery point.
func (t *TaskTrackerService) SetPersistence(db *gorm.DB) {
	if t == nil || db == nil {
		return
	}
	t.mu.Lock()
	t.db = db
	now := t.currentTime()
	t.mu.Unlock()
	err := db.Model(&model.BackgroundTaskRecord{}).
		Where("status = ?", TaskStatusRunning).
		Updates(map[string]any{"status": TaskStatusInterrupted, "error": "服务在任务完成前重启；请检查文件状态后重新执行", "updated_at": now, "finished_at": now}).Error
	if err != nil && t.log != nil {
		t.log.Warn("recover interrupted background tasks failed", zap.Error(err))
	}
}

type TaskHandle struct {
	tracker *TaskTrackerService
	id      string
}

func NewTaskTrackerService(log *zap.Logger, hub *Hub) *TaskTrackerService {
	return &TaskTrackerService{
		log:       log,
		hub:       hub,
		active:    make(map[string]*BackgroundTask),
		maxRecent: 30,
		now:       time.Now,
	}
}

func (t *TaskTrackerService) Start(kind, name string, update TaskUpdate) *TaskHandle {
	if t == nil {
		return nil
	}
	now := t.currentTime()
	task := &BackgroundTask{
		ID:         uuid.NewString(),
		Kind:       kind,
		Name:       name,
		Status:     TaskStatusRunning,
		Stage:      update.Stage,
		SourcePath: update.SourcePath,
		DestPath:   update.DestPath,
		Message:    update.Message,
		Metrics:    cloneTaskMetrics(update.Metrics),
		StartedAt:  now,
		UpdatedAt:  now,
	}
	applyTaskUpdate(task, update)
	t.mu.Lock()
	t.active[task.ID] = task
	snapshot := cloneBackgroundTask(*task)
	t.persist(snapshot)
	t.mu.Unlock()
	t.publish(snapshot)
	return &TaskHandle{tracker: t, id: task.ID}
}

func (h *TaskHandle) Update(update TaskUpdate) {
	if h == nil || h.tracker == nil {
		return
	}
	h.tracker.update(h.id, update)
}

func (h *TaskHandle) Finish(err error, update TaskUpdate) {
	if h == nil || h.tracker == nil {
		return
	}
	h.tracker.finish(h.id, err, update)
}

func (t *TaskTrackerService) Snapshot() TaskSnapshot {
	if t == nil {
		return TaskSnapshot{}
	}
	t.mu.Lock()
	active := make([]BackgroundTask, 0, len(t.active))
	for _, task := range t.active {
		active = append(active, cloneBackgroundTask(*task))
	}
	recent := make([]BackgroundTask, 0, len(t.recent))
	db := t.db
	limit := t.maxRecent
	if limit < 1 {
		limit = 30
	}
	for _, task := range t.recent {
		recent = append(recent, cloneBackgroundTask(task))
	}
	t.mu.Unlock()
	if db != nil {
		var rows []model.BackgroundTaskRecord
		if err := db.Where("status <> ?", TaskStatusRunning).Order("started_at DESC").Limit(limit).Find(&rows).Error; err == nil {
			recent = make([]BackgroundTask, 0, len(rows))
			for _, row := range rows {
				recent = append(recent, backgroundTaskFromRecord(row))
			}
		} else if t.log != nil {
			t.log.Warn("load background task history failed; using memory history", zap.Error(err))
		}
	}
	return TaskSnapshot{Active: active, Recent: recent}
}

func backgroundTaskFromRecord(row model.BackgroundTaskRecord) BackgroundTask {
	task := BackgroundTask{
		ID: row.ID, Kind: row.Kind, Name: row.Name, Status: row.Status,
		Stage: row.Stage, SourcePath: row.SourcePath, DestPath: row.DestPath,
		Message: row.Message, Error: row.Error, StartedAt: row.StartedAt,
		UpdatedAt: row.UpdatedAt, FinishedAt: row.FinishedAt,
	}
	_ = json.Unmarshal([]byte(row.Details), &task.Details)
	_ = json.Unmarshal([]byte(row.Metrics), &task.Metrics)
	_ = json.Unmarshal([]byte(row.Items), &task.Items)
	return task
}

func (t *TaskTrackerService) update(id string, update TaskUpdate) {
	now := t.currentTime()
	t.mu.Lock()
	task, ok := t.active[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	applyTaskUpdate(task, update)
	task.UpdatedAt = now
	snapshot := cloneBackgroundTask(*task)
	t.persist(snapshot)
	t.mu.Unlock()
	t.publish(snapshot)
}

func (t *TaskTrackerService) finish(id string, err error, update TaskUpdate) {
	now := t.currentTime()
	t.mu.Lock()
	task, ok := t.active[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	applyTaskUpdate(task, update)
	task.UpdatedAt = now
	task.FinishedAt = &now
	if err != nil {
		task.Status = TaskStatusFailed
		task.Error = err.Error()
	} else {
		task.Status = TaskStatusCompleted
	}
	delete(t.active, id)
	snapshot := cloneBackgroundTask(*task)
	t.recent = append([]BackgroundTask{snapshot}, t.recent...)
	if t.maxRecent <= 0 {
		t.maxRecent = 30
	}
	if len(t.recent) > t.maxRecent {
		t.recent = t.recent[:t.maxRecent]
	}
	notifier := t.failureNotifier
	t.persist(snapshot)
	t.mu.Unlock()
	t.publish(snapshot)

	// 失败通知放在锁外发送：网络请求绝不能阻塞任务状态机。
	if err != nil && notifier != nil {
		notifier(context.Background(), formatTaskFailureAlert(snapshot))
	}
}

func (t *TaskTrackerService) persist(task BackgroundTask) {
	if t == nil || t.db == nil {
		return
	}
	encode := func(value any) string {
		data, _ := json.Marshal(value)
		return string(data)
	}
	row := model.BackgroundTaskRecord{
		ID: task.ID, Kind: task.Kind, Name: task.Name, Status: task.Status,
		Stage: task.Stage, SourcePath: task.SourcePath, DestPath: task.DestPath,
		Message: task.Message, Error: task.Error, Details: encode(task.Details),
		Metrics: encode(task.Metrics), Items: encode(task.Items), StartedAt: task.StartedAt,
		UpdatedAt: task.UpdatedAt, FinishedAt: task.FinishedAt,
	}
	if err := t.db.Save(&row).Error; err != nil && t.log != nil {
		t.log.Warn("persist background task failed", zap.String("task_id", task.ID), zap.Error(err))
	}
}

// SetFailureNotifier 注入任务失败时的管理员通知回调。
func (t *TaskTrackerService) SetFailureNotifier(fn func(ctx context.Context, text string)) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.failureNotifier = fn
	t.mu.Unlock()
}

// formatTaskFailureAlert 生成面向管理员的失败摘要。错误信息可能很长
// （ffmpeg 输出等），这里截断，避免超出 Telegram 单条消息长度。
func formatTaskFailureAlert(task BackgroundTask) string {
	const maxErrRunes = 500
	text := strings.TrimSpace(task.Error)
	runes := []rune(text)
	if len(runes) > maxErrRunes {
		text = string(runes[:maxErrRunes]) + "…"
	}
	var b strings.Builder
	b.WriteString("⚠️ 任务失败：<b>")
	b.WriteString(html.EscapeString(task.Name))
	b.WriteString("</b>")
	if task.SourcePath != "" {
		b.WriteString("\n来源：")
		b.WriteString(html.EscapeString(task.SourcePath))
	}
	if text != "" {
		b.WriteString("\n错误：")
		b.WriteString(html.EscapeString(text))
	}
	return b.String()
}

func (t *TaskTrackerService) currentTime() time.Time {
	if t != nil && t.now != nil {
		return t.now()
	}
	return time.Now()
}

func (t *TaskTrackerService) publish(task BackgroundTask) {
	if t == nil || t.hub == nil {
		return
	}
	t.hub.Publish("task", task)
}

func applyTaskUpdate(task *BackgroundTask, update TaskUpdate) {
	if update.Stage != "" {
		task.Stage = update.Stage
	}
	if update.SourcePath != "" {
		task.SourcePath = update.SourcePath
	}
	if update.DestPath != "" {
		task.DestPath = update.DestPath
	}
	if update.Message != "" {
		task.Message = update.Message
	}
	if update.Details != nil {
		task.Details = append([]string(nil), update.Details...)
	}
	if update.Metrics != nil {
		task.Metrics = cloneTaskMetrics(update.Metrics)
	}
	if update.Items != nil {
		task.Items = append([]TaskItemRecord(nil), update.Items...)
	}
}

func cloneBackgroundTask(task BackgroundTask) BackgroundTask {
	task.Metrics = cloneTaskMetrics(task.Metrics)
	if task.Details != nil {
		task.Details = append([]string(nil), task.Details...)
	}
	if task.Items != nil {
		task.Items = append([]TaskItemRecord(nil), task.Items...)
	}
	if task.FinishedAt != nil {
		finishedAt := *task.FinishedAt
		task.FinishedAt = &finishedAt
	}
	return task
}

func cloneTaskMetrics(metrics map[string]int64) map[string]int64 {
	if len(metrics) == 0 {
		return nil
	}
	out := make(map[string]int64, len(metrics))
	for key, value := range metrics {
		out[key] = value
	}
	return out
}
