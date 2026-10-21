type HlsVideo = Pick<HTMLVideoElement, 'src' | 'currentTime' | 'addEventListener' | 'removeEventListener' | 'play'>
type PendingSeek = { current: number | null }

/** Local mid-file restarts may wait 120 seconds on the server before a playlist exists. */
export function hlsManifestTimeoutMs(source: 'cloud' | 'local', startSec: number): number {
  return source === 'local' && startSec > 0.05 ? 135_000 : 60_000
}

/** Keep a pending seek until the media element accepts it; metadata can arrive later. */
export function applyPendingHlsSeek(video: Pick<HlsVideo, 'currentTime'>, pending: PendingSeek): boolean {
  if (pending.current === null) return true
  if (!Number.isFinite(pending.current) || pending.current < 0) return false
  try {
    video.currentTime = pending.current
    pending.current = null
    return true
  } catch {
    return false
  }
}

export function bindPendingHlsSeek(video: HlsVideo, pending: PendingSeek): () => void {
  const restore = () => { applyPendingHlsSeek(video, pending) }
  video.addEventListener('loadedmetadata', restore)
  video.addEventListener('canplay', restore)
  return () => {
    video.removeEventListener('loadedmetadata', restore)
    video.removeEventListener('canplay', restore)
  }
}

/** Native HLS needs the same seek restoration as hls.js, with removable listeners. */
export function attachNativeHls(video: HlsVideo, url: string, pending: PendingSeek): () => void {
  const detach = bindPendingHlsSeek(video, pending)
  video.src = url
  void video.play().catch(() => undefined)
  return detach
}

/** Only locally transcoded HLS uses a relative timeline starting at startSec. */
export function playbackPositionSec(mode: 'direct' | 'hls', source: 'cloud' | 'local', startSec: number, currentTime: number): number {
  return mode === 'hls' && source === 'local' ? startSec + currentTime : currentTime
}

/** Convert an absolute source target to the current media element's timeline. */
export function playbackTimeSec(mode: 'direct' | 'hls', source: 'cloud' | 'local', startSec: number, absoluteSec: number): number {
  return Math.max(0, mode === 'hls' && source === 'local' ? absoluteSec - startSec : absoluteSec)
}
