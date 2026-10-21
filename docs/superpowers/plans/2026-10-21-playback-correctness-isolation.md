# Playback Correctness and Isolation Implementation Plan

**Goal:** Fix the five playback defects from the 2026-10-21 review and make playback capability queries side-effect-free, without replacing the player or adding dependencies.

**Scope:** The user approved implementing the reviewed fixes. Work in the current clean checkout; do not commit, push, deploy, or alter unrelated files.

## Design and invariants

- Keep existing media IDs, stream routes, direct playback and external-player URLs compatible.
- Add optional `session_id` to local HLS requests. Authenticated handlers supply the actual user identity; a client parameter never supplies the owner. New web playback sessions use random IDs. Legacy clients are isolated by user and, where available, Emby play-session/device identity.
- Keep transcoder media lookup/logging separate from its internal task key. A task key incorporates user, media and playback session. Each actual ffmpeg run writes to a fresh generation directory, so delayed segment requests never read a replacement run's output.
- Stop only the authenticated caller's playback session. Check media visibility and scoped playback tokens on stop, just as on playlist/segment requests. Segment and playlist queries must match the running generation.
- Cloud child requests resolve their real session media first, compare query media, enforce token scope and visibility against the real media, and reject mismatches before upstream fetch.
- Treat player business positions as absolute source seconds. Only local HLS converts to/from a relative ffmpeg timeline.
- Invalidate outstanding cloud-transcode requests on media/unmount, explicit source/quality changes, and newer requests. Guard both fulfilled and rejected responses.
- Select local fallbacks only from available local qualities. Keep the existing 115-first preference when a cloud quality is available.
- `PlaybackInfo` only reads capabilities/status. Explicit `StartTranscode` requests enqueue cloud work. Retain pending/ready response contracts, polling, and retries.

## Tasks

1. Add regression tests for local-scope isolation, stale generations, stop authorization, cloud media binding, pure capability reads, timeline conversion, available-local selection and stale async results. Run tests before implementation and verify the intended failures.
2. Add tested playback helpers; route skip/undo/progress/quality/subtitle calculations through the common absolute timeline; guard cloud transcode results and fix local fallback choice.
3. Extend existing transcoder/stream methods with an optional scope, preserve legacy internal calls, isolate task keys/output directories, and pass caller scope from web and Emby handlers. Bind web local HLS URLs and stop requests to captured session IDs.
4. Enforce real-media authorization on cloud child requests and authenticated ownership on local stop.
5. Split read-only cloud capability lookup from explicit cloud enqueue. Update existing cloud tests to use the explicit start operation and test no push during reads.
6. Run focused and full Go tests, frontend tests, TypeScript/Vite build, lint diagnostics and review the diff for security, lifecycle and API compatibility. Fix regressions before reporting completion.

## Verification commands

- `node --experimental-strip-types --test src/pages/hlsPlayback.test.ts src/pages/directPlaySeek.test.ts src/pages/playerPageModel.test.ts src/pages/playbackQuality.test.ts src/pages/playbackRequestGuard.test.ts src/utils/skipSegments.test.ts`
- `go test ./internal/service ./internal/handler ./internal/middleware -count=1`
- `go test ./...`
- `npm run build` and targeted ESLint for changed frontend files.

## Out of scope

New remux/audio-only ffmpeg pipelines, speculative browser codec policy changes, player replacement, broad UI refactors, performance improvement claims without measurements, and production deployment.

## Verification outcome

- Six frontend playback regression scripts pass; targeted ESLint and the TypeScript/Vite production build pass. The existing bundle-size warning remains.
- Playback/stream/transcode/progress tests in service, handler and middleware pass, including scoped output reads, stale generation rejection, scoped stops, device-to-segment propagation, read-only capability queries and concurrent cloud start deduplication.
- Whole-repository tests pass with only `TestQueueLibraryRootScanImportsNewLibraryMedia` and `TestSQLiteRestoreStagesWithoutChangingLiveDatabase` excluded. Both were run independently and reproduce Windows file-lock / SQLite errors in unmodified scan/database code. They are not silently disabled in source.
- FFmpeg is not installed in this environment, so the existing real-transcode lifecycle test skips. Actual video, upstream 115 and browser end-to-end playback remain unverified.
- Clients with no playback-session or device identifier retain per-user legacy isolation; new web requests always carry an independent session ID.
- No commit, push or deployment was performed.
