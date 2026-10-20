export type DirectPlayErrorAction = 'ignore' | 'retry' | 'fallback'

const MEDIA_ERR_ABORTED = 1
const HAVE_CURRENT_DATA = 2

/** 程序化续播/跳转后忽略直连误报的宽限期（毫秒）。 */
export const DIRECT_SEEK_GRACE_MS = 12_000

/** 直连起播看门狗的等待上限（毫秒）。 */
export const DIRECT_STALL_TIMEOUT_MS = 6_000
/** STRM / 云盘直链要等 302 换链，冷启动比本地文件慢，给长一点的期限。 */
export const DIRECT_STALL_TIMEOUT_STRM_MS = 12_000
/**
 * 没有可退线路（远程 Emby 挂载、客户端直连解码）时的等待上限。
 *
 * 这两种源都不在宿主机转码，等不到数据时没有任何补救动作——唯一的输出是给用户
 * 一条提示，所以宁可多等：跨网上游的冷启动实测要 8–11 秒才吐第一段数据，6 秒
 * 就判失败只会把「上游慢」写成「浏览器不支持这个编码」，而画面其实几秒后就播上了。
 */
export const DIRECT_STALL_TIMEOUT_NO_FALLBACK_MS = 20_000

/**
 * 起播看门狗该等多久才把「还没起来」当成「起不来」。
 *
 * canFallback=false：直连失败没有可切的线路（远程 Emby / 直连解码），失败提示
 * 是唯一产出，所以要容忍慢启动。canFallback=true：还能切 HLS，早点换线路对用户
 * 更有利，保持较短期限。
 */
export function directStallTimeoutMs(input: { isStrm?: boolean; canFallback: boolean }): number {
  if (!input.canFallback) return DIRECT_STALL_TIMEOUT_NO_FALLBACK_MS
  return input.isStrm ? DIRECT_STALL_TIMEOUT_STRM_MS : DIRECT_STALL_TIMEOUT_MS
}

/** 直连起播失败的两类起因：看门狗等超时（上游一直没数据）/ 媒体元素真的报了 error。 */
export type DirectPlayFailureReason = 'stall' | 'error'

/** 直连失败时用户看到的来源：远程 Emby 挂载 / 客户端直连解码。 */
export type DirectPlayFailureKind = 'remote-emby' | 'direct-only'

/**
 * 直连失败的横幅与 toast 文案。
 *
 * 超时和真报错的文案必须分开：超时的真实原因是上游慢，不该断言「浏览器不支持这个
 * 编码」（实测最常见的情形就是慢，画面随后自己就播上了）。
 */
export function directPlayFailureCopy(
  kind: DirectPlayFailureKind,
  reason: DirectPlayFailureReason = 'error',
): { banner: string; toast: string } {
  if (reason === 'stall') {
    return {
      banner:
        kind === 'remote-emby'
          ? '直接播放失败：远程 Emby 挂载的流长时间没有返回数据（该线路不转码，只能直连）。可稍后重试，或用外部播放器（如 PotPlayer / VLC / IINA）播放。'
          : '直接播放失败：客户端直连解码模式下宿主机不转码，流长时间没有返回数据。可稍后重试，或关闭直连解码模式改用转码播放。',
      toast: '直连流长时间没有响应',
    }
  }
  return {
    banner:
      kind === 'remote-emby'
        ? '直接播放失败。该媒体为远程 Emby 挂载直连播放（不进行转码）；当前浏览器可能不支持该视频编码或音频格式，建议使用外部播放器（如 PotPlayer / VLC / IINA）播放。'
        : '直接播放失败。当前为「客户端直连解码」模式，宿主机不转码；请使用支持该编码/封装的播放器（如 Infuse / VLC / Emby 客户端）播放，或关闭直连解码模式。',
    toast: '直接播放失败，建议使用外部播放器',
  }
}

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
