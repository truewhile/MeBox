export type DirectPlayErrorAction = 'ignore' | 'retry' | 'fallback'

const MEDIA_ERR_ABORTED = 1
const HAVE_CURRENT_DATA = 2

/**
 * Chromium 常把被中断的直连（换 src、插入 track、302 未完成就 seek）
 * 报成 error。真正不兼容应 fallback；瞬时中断应忽略或静默重试一次。
 */
export function classifyDirectPlayError(input: {
  errorCode: number | undefined | null
  readyState: number
  elementSrc: string
  expectedSrc: string
  alreadyRetried: boolean
}): DirectPlayErrorAction {
  const code = input.errorCode ?? 0
  if (code === MEDIA_ERR_ABORTED) return 'ignore'
  if (input.readyState >= HAVE_CURRENT_DATA) return 'ignore'
  if (!input.elementSrc) return 'ignore'
  if (input.expectedSrc && input.elementSrc !== input.expectedSrc) return 'ignore'
  if (!input.alreadyRetried) return 'retry'
  return 'fallback'
}
