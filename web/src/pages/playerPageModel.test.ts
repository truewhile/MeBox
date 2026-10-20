import {
  classifyDirectPlayError,
  directPlayFailureCopy,
  directStallTimeoutMs,
  DIRECT_STALL_TIMEOUT_MS,
  DIRECT_STALL_TIMEOUT_NO_FALLBACK_MS,
  DIRECT_STALL_TIMEOUT_STRM_MS,
} from './directPlayError.ts'

function check(name: string, cond: boolean) {
  if (!cond) throw new Error(`classifyDirectPlayError: ${name}`)
}

const src = 'http://nas.local/api/stream/m1?token=abc'

check(
  'aborted load is ignored',
  classifyDirectPlayError({
    errorCode: 1,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'already-playing error is ignored',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 3,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'stale src error is ignored',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: 'http://nas.local/old',
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'empty src during reload is ignored',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: '',
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'ignore',
)

check(
  'first decode/network miss retries',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
  }) === 'retry',
)

check(
  'second failure falls back to HLS',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: true,
  }) === 'fallback',
)

check(
  'seek grace ignores decode miss without retrying',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: false,
    seekGraceActive: true,
  }) === 'ignore',
)

check(
  'seek grace never falls back to HLS',
  classifyDirectPlayError({
    errorCode: 4,
    readyState: 0,
    elementSrc: src,
    expectedSrc: src,
    alreadyRetried: true,
    seekGraceActive: true,
  }) === 'ignore',
)

// 起播看门狗的期限：没有可退线路（远程 Emby / 直连解码）时必须容忍慢启动，
// 否则上游冷启动的 8–11 秒会被误报成「编码不兼容」。
check('local direct stall waits 6s', directStallTimeoutMs({ canFallback: true }) === DIRECT_STALL_TIMEOUT_MS)
check(
  'strm direct stall waits 12s',
  directStallTimeoutMs({ isStrm: true, canFallback: true }) === DIRECT_STALL_TIMEOUT_STRM_MS,
)
check(
  'no fallback line waits 20s',
  directStallTimeoutMs({ canFallback: false }) === DIRECT_STALL_TIMEOUT_NO_FALLBACK_MS,
)
check(
  'no fallback line ignores strm tuning',
  directStallTimeoutMs({ isStrm: true, canFallback: false }) === DIRECT_STALL_TIMEOUT_NO_FALLBACK_MS,
)

const stallCopy = directPlayFailureCopy('remote-emby', 'stall')
check('stall copy blames the upstream, not the codec', !stallCopy.banner.includes('不支持'))
check('stall copy keeps the failure prefix', stallCopy.banner.startsWith('直接播放失败'))
const decodeCopy = directPlayFailureCopy('remote-emby', 'error')
check('decode copy still mentions unsupported codec', decodeCopy.banner.includes('不支持'))
check(
  'copy defaults to the decode wording',
  directPlayFailureCopy('remote-emby').banner === decodeCopy.banner,
)
check(
  'direct-only copy names the client-decode mode',
  directPlayFailureCopy('direct-only', 'stall').banner.includes('客户端直连解码') &&
    directPlayFailureCopy('direct-only', 'error').banner.includes('客户端直连解码'),
)
check('stall toast is not the external-player advice', stallCopy.toast !== decodeCopy.toast)

console.log('playerPageModel.test.ts ok')
