export type DirectPlayErrorAction = 'ignore' | 'retry' | 'fallback'

const MEDIA_ERR_ABORTED = 1
const HAVE_CURRENT_DATA = 2

/** 程序化续播/跳转后忽略直连误报的宽限期（毫秒）。 */
export const DIRECT_SEEK_GRACE_MS = 12_000

/**
 * Chromium 常把被中断的直连（换 src、插入 track、302 未完成就 seek）
 * 报成 error。真正不兼容应 fallback；瞬时中断应忽略或静默重试一次。
 *
 * seekGraceActive：程序化续播/跳转后的宽限期。115 STRM 在 302 直链上
 * seek 时 Chromium 经常误报 decode/network error，此时绝不能升到 HLS。
 */
export function classifyDirectPlayError(input: {
  errorCode: number | undefined | null
  readyState: number
  elementSrc: string
  expectedSrc: string
  alreadyRetried: boolean
  seekGraceActive?: boolean
}): DirectPlayErrorAction {
  const code = input.errorCode ?? 0
  if (code === MEDIA_ERR_ABORTED) return 'ignore'
  if (input.readyState >= HAVE_CURRENT_DATA) return 'ignore'
  if (!input.elementSrc) return 'ignore'
  if (input.expectedSrc && input.elementSrc !== input.expectedSrc) return 'ignore'
  // 续播 seek 宽限期内：误报很常见，且 retry 会 video.load() 把进度清零，直接忽略。
  if (input.seekGraceActive) return 'ignore'
  if (!input.alreadyRetried) return 'retry'
  return 'fallback'
}
