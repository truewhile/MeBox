package service

import (
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/truewhile/MeBox/internal/model"
)

func TestTaskTrackerPersistsHistoryAcrossRestart(t *testing.T) {
	db := newServiceTestDB(t)
	tracker := NewTaskTrackerService(zap.NewNop(), nil)
	tracker.SetPersistence(db)
	task := tracker.Start(TaskKindScan, "scan", TaskUpdate{Details: []string{"initial detail"}})
	task.Update(TaskUpdate{Metrics: map[string]int64{"visited": 42}})
	task.Finish(nil, TaskUpdate{Message: "done"})
	restarted := NewTaskTrackerService(zap.NewNop(), nil)
	restarted.SetPersistence(db)
	snapshot := restarted.Snapshot()
	if len(snapshot.Active) != 0 || len(snapshot.Recent) != 1 {
		t.Fatalf("unexpected history: %#v", snapshot)
	}
	recent := snapshot.Recent[0]
	if recent.Status != TaskStatusCompleted || recent.Metrics["visited"] != 42 || len(recent.Details) != 1 || recent.FinishedAt == nil {
		t.Fatalf("task history lost fields: %#v", recent)
	}
}

func TestTaskTrackerConcurrentUpdatesDoNotRegressFinishedTasks(t *testing.T) {
	db := newServiceTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	tracker := NewTaskTrackerService(zap.NewNop(), nil)
	tracker.SetPersistence(db)
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			task := tracker.Start(TaskKindScan, fmt.Sprintf("scan-%d", index), TaskUpdate{})
			task.Update(TaskUpdate{Metrics: map[string]int64{"visited": 1}})
			task.Finish(nil, TaskUpdate{})
			task.Update(TaskUpdate{Message: "late update"})
			_ = tracker.Snapshot()
		}(i)
	}
	workers.Wait()
	var completed int64
	if err := db.Model(&model.BackgroundTaskRecord{}).Where("status = ?", TaskStatusCompleted).Count(&completed).Error; err != nil || completed != 12 {
		t.Fatalf("finished task status regressed: completed=%d err=%v", completed, err)
	}
	if snapshot := tracker.Snapshot(); len(snapshot.Active) != 0 || len(snapshot.Recent) != 12 {
		t.Fatalf("inconsistent task snapshot: %#v", snapshot)
	}
}

func TestTaskTrackerMarksAbandonedTasksInterrupted(t *testing.T) {
	db := newServiceTestDB(t)
	tracker := NewTaskTrackerService(zap.NewNop(), nil)
	tracker.SetPersistence(db)
	tracker.Start(TaskKindOrganize, "unfinished", TaskUpdate{})
	restarted := NewTaskTrackerService(zap.NewNop(), nil)
	restarted.SetPersistence(db)
	snapshot := restarted.Snapshot()
	if len(snapshot.Recent) != 1 || snapshot.Recent[0].Status != TaskStatusInterrupted || snapshot.Recent[0].FinishedAt == nil {
		t.Fatalf("abandoned task not marked interrupted: %#v", snapshot)
	}
	var running int64
	if err := db.Model(&model.BackgroundTaskRecord{}).Where("status = ?", TaskStatusRunning).Count(&running).Error; err != nil || running != 0 {
		t.Fatalf("stale running tasks: %d err=%v", running, err)
	}
}
