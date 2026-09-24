# Segment Coverage Improvements (1–4, option C)

## Goal

Raise intro/outro skip availability without Chromaprint or full-library STRM probing.

## Scope (approved)

1. **IntroDB prewarm** — background job for all queryable media (TMDb + season/episode).
2. **Duration** — playback-path only (no full STRM scan); keep `ListForPlayback` → `EnsureAsync`.
3. **Same-season intro propagation** — copy intro to sibling episodes after a hit.
4. **Negative cache** — miss TTL 24h; 403/429/timeout must not write miss ledger.

## Non-goals

Chromaprint, manual segment UI, library-name filters, batch STRM duration scan.

## Behavior

### Prewarm (`segment_prewarm`)

- Scheduler job, interval 6h, initial delay 6h (avoid restart spike).
- **Opt-in via `segment.prewarm_enabled` (default off)** — system settings toggle「后台预热 IntroDB 片头片段」.
- Candidates: `tm_db_id > 0`, season/episode or movie, ledger missing or stale.
- Order: recently played first, then others.
- Rate: ~1 req/s, stop on context cancel; respect IntroDB 429 retry already in client.
- Reuse `MediaSegmentService` refresh + propagation.
- Manual `RunNow` can bypass the toggle (same pattern as organize).

### Propagation

- After TheIntroDB refresh finds at least one `intro` span, write the same intro window to same-season siblings that lack a `theintrodb` intro.
- Source tag: `propagated`.
- Do not propagate credits/preview/recap.
- Playback merge priority per kind: `manual` > `theintrodb` > `propagated`.

### Negative cache

- `segmentMissingTTL`: 24h (was 7d).
- `segmentFoundTTL`: 30d (unchanged).
- Provider errors (non-404) continue to skip ledger writes.

### Duration (C)

- No new batch probe job.
- Existing `defer ensureMediaProbe` on playback remains the only STRM duration path.

## Success criteria

- Prewarm increases `media_segment_fetches` over time without blocking play.
- One IntroDB intro hit can surface skip on sibling episodes via `propagated`.
- Misses are retried within ~24h (sooner if recently played and prewarm runs).
