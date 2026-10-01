import { resolveActiveSkip, skippedNoticeText, skipLabel, toSkipSegments } from './skipSegments.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`skipSegments: ${name}`)
}

function equal<T>(name: string, actual: T, expected: T) {
  if (actual !== expected) {
    throw new Error(`skipSegments: ${name} — got ${String(actual)}, want ${String(expected)}`)
  }
}

// ── toSkipSegments ──────────────────────────────────────────────────────────

check('空片段返回空数组', toSkipSegments(null, 600).length === 0)
check('空数组返回空数组', toSkipSegments([], 600).length === 0)

// end_ms: 0 表示延续到片尾，用媒体时长补齐。
const resolved = toSkipSegments(
  [
    { kind: 'intro', start_ms: 228_664, end_ms: 246_143 },
    { kind: 'credits', start_ms: 3_431_000, end_ms: 0 },
  ],
  3600,
)
equal('区间数量', resolved.length, 2)
equal('片头起点(秒)', resolved[0].startSec, 228.664)
equal('片头终点(秒)', resolved[0].endSec, 246.143)
equal('片尾终点补齐为总时长', resolved[1].endSec, 3600)

// 时长未知时，开放区间必须丢弃：否则会算出一个永远命中的区间。
check(
  '时长未知时丢弃开放区间',
  toSkipSegments([{ kind: 'credits', start_ms: 1000, end_ms: 0 }], 0).length === 0,
)

// 无效区间（end <= start）没有任何可跳过内容，不能生成按钮。
check(
  '丢弃空区间',
  toSkipSegments(
    [
      { kind: 'intro', start_ms: 5000, end_ms: 5000 },
      { kind: 'recap', start_ms: 9000, end_ms: 8000 },
    ],
    600,
  ).length === 0,
)

// 服务端已按类型排序，但客户端仍要按实际时间排，避免同类型多段时顺序错乱。
const ordered = toSkipSegments(
  [
    { kind: 'credits', start_ms: 900_000, end_ms: 960_000 },
    { kind: 'intro', start_ms: 100_000, end_ms: 200_000 },
  ],
  3600,
)
equal('按起点排序', ordered[0].kind, 'intro')

// ── resolveActiveSkip ───────────────────────────────────────────────────────

const segments = toSkipSegments(
  [
    { kind: 'intro', start_ms: 100_000, end_ms: 200_000 },
    { kind: 'credits', start_ms: 3_400_000, end_ms: 3_500_000 },
  ],
  3600,
)

check('区间之前不提示', resolveActiveSkip(50, segments) === null)
check('区间之中提示', resolveActiveSkip(150, segments)?.kind === 'intro')
check('区间结束后不提示', resolveActiveSkip(250, segments) === null)
check('起点即提示', resolveActiveSkip(100, segments)?.kind === 'intro')
check('终点视为已离开(右开区间)', resolveActiveSkip(200, segments) === null)
check('容差内提前提示', resolveActiveSkip(99.9, segments)?.kind === 'intro')
check('非有限值不提示', resolveActiveSkip(Number.NaN, segments) === null)

// 撤消过的类型不再提示；同一位置的其它类型不受影响。
check(
  '排除已处理的类型',
  resolveActiveSkip(150, segments, { excludedKinds: ['intro'] }) === null,
)
check(
  '排除只影响指定类型',
  resolveActiveSkip(3450, segments, { excludedKinds: ['intro'] })?.kind === 'credits',
)

// ── 文案 ────────────────────────────────────────────────────────────────────

equal('片头文案', skipLabel('intro', false), '跳过片头')
equal('回顾也归入片头', skipLabel('recap', false), '跳过片头')
equal('最后一集的片尾', skipLabel('credits', false), '跳过片尾')
equal('有下一集时片尾变成下一集', skipLabel('credits', true), '下一集')
equal('预告同理', skipLabel('preview', true), '下一集')
equal(
  '提示里带上有下一集的文案',
  resolveActiveSkip(3450, segments, { hasNextEpisode: true })?.label,
  '下一集',
)

// ── 自动跳过后的提示文案 ────────────────────────────────────────────────────

equal('片头提示', skippedNoticeText('intro'), '已跳过片头')
equal('回顾提示', skippedNoticeText('recap'), '已跳过回顾')
equal('片尾提示', skippedNoticeText('credits'), '已跳过片尾')
equal('预告按片尾处理', skippedNoticeText('preview'), '已跳过片尾')

console.log('skipSegments.test.ts ok')
