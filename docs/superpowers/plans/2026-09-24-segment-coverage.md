# Segment Coverage Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** IntroDB prewarm + same-season intro propagation + shorter miss TTL + playback-only duration probe (option C).

**Architecture:** Extend `MediaSegmentService` for merge/propagate/prewarm; add scheduler job `segment_prewarm`; repository queries for candidates and season siblings.

**Tech Stack:** Go, GORM/SQLite, existing SchedulerService + IntroDB client.

---

### Task 1: Negative cache TTL + ledgerFresh tests

**Files:** `internal/service/media_segment.go`, `internal/service/media_segment_test.go`

- Change `segmentMissingTTL` to 24h; update tests for fresh/stale miss.

### Task 2: Playback merge + propagation

**Files:** `media_segment.go`, `media_segment_repository.go`, `media_repository.go`, tests

- Constant `PropagatedSource = "propagated"`.
- `ListForPlayback` merges sources with priority manual > theintrodb > propagated.
- After successful IntroDB refresh with intro, propagate to same-season siblings (shared `tm_db_id` or `series_id`).

### Task 3: Prewarm API + scheduler job

**Files:** `media_segment.go`, `media_segment_repository.go`, `scheduler.go`, `scheduler_segment_jobs.go`, `service_builder.go`, tests

- `Prewarm(ctx, limit)` rate-limited refreshes.
- Job `segment_prewarm` every 6h.

### Task 4: Verify

- `go test` for affected packages.
