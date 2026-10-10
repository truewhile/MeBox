import {
  SCROLL_SAVE_THRESHOLD_PCT,
  formatScrollPct,
  parseLastScrollPct,
  shouldSaveScrollProgress,
} from './readerScrollProgress.ts'

function check(name: string, condition: boolean) {
  if (!condition) throw new Error(`readerScrollProgress: ${name}`)
}

// 回归：dataset.last 缺失时 Number(undefined) 是 NaN，旧实现用 `?? -1` 接不住，
// 比较恒为 false，滚动进度永远不保存。首次滚动必须触发。
check('missing dataset triggers the first save', shouldSaveScrollProgress(0.1, undefined) === true)

// 空串同样要按「没有历史值」处理。
check('empty dataset triggers save', shouldSaveScrollProgress(0.1, '') === true)

// 变化不足阈值不保存（避免每帧都上报）。
check('small delta is throttled', shouldSaveScrollProgress(0.101, '0.1') === false)

// 达到阈值保存。
check('threshold delta saves', shouldSaveScrollProgress(0.13, '0.1') === true)
check('backward scroll saves', shouldSaveScrollProgress(0.07, '0.1') === true)

// 边界：正好等于阈值。
const justAt = 0.1 + SCROLL_SAVE_THRESHOLD_PCT / 100
check('exactly at threshold saves', shouldSaveScrollProgress(justAt, '0.1') === true)

// 非法输入不触发保存。
check('non-finite current pct is ignored', shouldSaveScrollProgress(NaN, '0.1') === false)
check('corrupt dataset value is treated as missing', shouldSaveScrollProgress(0.5, 'not-a-number') === true)

// 解析与格式化往返。
check('parse keeps finite values', parseLastScrollPct('0.25') === 0.25)
check('parse rejects garbage', Number.isNaN(parseLastScrollPct('x')))
check('format round-trips', parseLastScrollPct(formatScrollPct(0.375)) === 0.375)

console.log('readerScrollProgress tests passed')
