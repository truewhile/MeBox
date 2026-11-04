import {
  canContinuePlaying,
  continueButtonLabel,
  episodeProgressById,
  formatResumeClock,
  progressPercent,
  resolveMediaDetailResume,
} from './mediaDetailResumeModel.ts'
import type { HistoryItem, Media } from '../types'

function check(name: string, cond: boolean) {
  if (!cond) throw new Error(`mediaDetailResumeModel: ${name}`)
}

function media(partial: Partial<Media> & { id: string }): Media {
  return {
    library_id: 'lib-1',
    title: '标题',
    path: `/media/${partial.id}.mkv`,
    size_bytes: 0,
    duration_sec: 0,
    width: 0,
    height: 0,
    rating: 0,
    year: 2024,
    season_num: 0,
    episode_num: 0,
    scrape_status: 'matched',
    tmdb_id: 0,
    bangumi_id: 0,
    nsfw: false,
    created_at: '2024-01-01T00:00:00Z',
    updated_at: '2024-01-01T00:00:00Z',
    ...partial,
  } as Media
}

function historyRow(
  mediaID: string,
  positionMs: number,
  watchedAt: string,
  extra: Partial<HistoryItem> = {},
): HistoryItem {
  return {
    id: `h-${mediaID}`,
    user_id: 'u1',
    media_id: mediaID,
    position_ms: positionMs,
    duration_ms: 1_440_000,
    watched_at: watchedAt,
    completed: false,
    ...extra,
  } as HistoryItem
}

// ── 电影：直接用本条的进度 ────────────────────────────────────────────────
const movie = media({ id: 'movie-1' })
const movieResume = resolveMediaDetailResume(movie, [], [historyRow('movie-1', 754_000, '2024-05-01T10:00:00Z')])
check('movie resume found', movieResume?.mediaId === 'movie-1')
check('movie label', continueButtonLabel(movieResume!) === '继续播放 · 12:34')
check('movie can continue', canContinuePlaying(movieResume))

// ── 剧集：代表条目是 OVA，续播要指向真正看过的那一集 ──────────────────────
const ova = media({ id: 'ova', episode_num: 0, title: '特别篇' })
const ep1 = media({ id: 'ep1', season_num: 1, episode_num: 1 })
const ep2 = media({ id: 'ep2', season_num: 1, episode_num: 2 })
const ep3 = media({ id: 'ep3', season_num: 1, episode_num: 3 })
const episodes = [ep1, ep2, ep3]
const seriesHistory = [
  historyRow('ep1', 600_000, '2024-05-01T09:00:00Z', { completed: true }),
  historyRow('ep2', 300_000, '2024-05-02T09:00:00Z'),
  historyRow('ep3', 0, '2024-05-03T09:00:00Z'),
  historyRow('other-show', 900_000, '2024-05-04T09:00:00Z'),
]
const seriesResume = resolveMediaDetailResume(ova, episodes, seriesHistory)
check('series picks newest watched episode', seriesResume?.mediaId === 'ep2')
check('series label', continueButtonLabel(seriesResume!) === '继续播放 · 第 2 集 · 5:00')

// 命中的是某一集的版本时，打开版本、但集号仍按它属于哪一集显示。
const ep2Uhd = media({ id: 'ep2-uhd', season_num: 1, episode_num: 2 })
const versionedEpisodes = [ep1, { ...ep2, versions: [ep2Uhd] }, ep3]
const versionResume = resolveMediaDetailResume(ova, versionedEpisodes, [
  historyRow('ep2-uhd', 120_000, '2024-05-02T09:00:00Z'),
])
check('version resume target', versionResume?.mediaId === 'ep2-uhd')
check('version resume label uses owning episode', versionResume?.media.episode_num === 2)

// ── 不值得续播的情况 ──────────────────────────────────────────────────────
check(
  'near-start is not a resume',
  !canContinuePlaying(resolveMediaDetailResume(movie, [], [historyRow('movie-1', 1_500, '2024-05-01T10:00:00Z')])),
)
check(
  'completed is not a resume',
  !canContinuePlaying(
    resolveMediaDetailResume(movie, [], [
      historyRow('movie-1', 1_400_000, '2024-05-01T10:00:00Z', { completed: true }),
    ]),
  ),
)
check('no history', resolveMediaDetailResume(movie, [], []) === null)
check(
  'unrelated history is ignored',
  resolveMediaDetailResume(movie, [], [historyRow('other', 900_000, '2024-05-01T10:00:00Z')]) === null,
)

// ── 时间格式 ──────────────────────────────────────────────────────────────
check('clock minutes', formatResumeClock(754_000) === '12:34')
check('clock hours', formatResumeClock(3_723_000) === '1:02:03')
check('clock invalid', formatResumeClock(0) === '' && formatResumeClock(Number.NaN) === '')

// ── 每集进度（剧集卡片上的进度条） ────────────────────────────────────────
const progress = episodeProgressById(versionedEpisodes, [
  historyRow('ep1', 700_000, '2024-05-01T09:00:00Z', { completed: true }),
  historyRow('ep2', 200_000, '2024-05-01T09:10:00Z'),
  historyRow('ep2-uhd', 400_000, '2024-05-01T09:20:00Z'),
])
check('episode progress skips untouched episodes', !progress.has('ep3'))
check('episode progress prefers newest version row', progress.get('ep2')?.positionMs === 400_000)
check('completed episode is full', progressPercent(progress.get('ep1')!) === 100)
check('partial episode is capped', progressPercent({ positionMs: 1_430_000, durationMs: 1_440_000, completed: false }) === 99)
check('unknown duration still visible', progressPercent({ positionMs: 10_000, durationMs: 0, completed: false }) === 5)
