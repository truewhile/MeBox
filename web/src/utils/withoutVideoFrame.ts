/**
 * JASSUB probes color space with `new VideoFrame(video)` during construct.
 * Direct-play 302s to a CDN / remote Emby taint <video>, and Chrome throws
 * SecurityError. Overlay rendering uses requestVideoFrameCallback metadata,
 * not pixels, so hide VideoFrame for the sync constructor call.
 *
 * ponytail: skips color-space matching for all ASS playback, not only tainted
 * sources. 302 completes after construct, so we cannot probe currentSrc first.
 * Upgrade: canvas-only JASSUB driven by rVFC metadata when the element taints.
 */
export function runWithoutGlobalVideoFrame<T>(fn: () => T): T {
  const desc = Object.getOwnPropertyDescriptor(globalThis, 'VideoFrame')
  Object.defineProperty(globalThis, 'VideoFrame', {
    configurable: true,
    writable: true,
    value: undefined,
  })
  try {
    return fn()
  } finally {
    if (desc) {
      Object.defineProperty(globalThis, 'VideoFrame', desc)
    } else {
      delete (globalThis as { VideoFrame?: unknown }).VideoFrame
    }
  }
}
