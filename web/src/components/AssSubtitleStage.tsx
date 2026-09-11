import { useEffect, useRef } from 'react'
import JASSUB from 'jassub'
import jassubWorkerUrl from 'jassub/dist/worker/worker.js?worker&url'
import jassubWasmUrl from 'jassub/dist/wasm/jassub-worker.wasm?url'
import jassubModernWasmUrl from 'jassub/dist/wasm/jassub-worker-modern.wasm?url'
import notoSansSCUrl from '../assets/fonts/noto-sans-sc-regular.woff2?url'

import {
  loadSubtitleChineseConverter,
  type SubtitleChineseMode,
} from '../utils/subtitleChinese'
import { convertASSContent } from '../utils/subtitleASS'

type AssSubtitleStageProps = {
  videoRef: React.RefObject<HTMLVideoElement>
  source: string
  timeOffset?: number
  chineseMode?: SubtitleChineseMode
  onReady?: () => void
  onError?: (error: unknown) => void
}

/**
 * libass-wasm subtitle layer for external and embedded ASS/SSA tracks. JASSUB
 * owns its canvas and attaches it next to the video, so this component itself
 * renders no visible DOM nodes.
 */
export function AssSubtitleStage({
  videoRef,
  source,
  timeOffset = 0,
  chineseMode = 'original',
  onReady,
  onError,
}: AssSubtitleStageProps) {
  const instanceRef = useRef<JASSUB | null>(null)
  const timeOffsetRef = useRef(timeOffset)
  const callbacksRef = useRef({ onReady, onError })

  useEffect(() => {
    callbacksRef.current = { onReady, onError }
  }, [onReady, onError])

  useEffect(() => {
    const video = videoRef.current
    if (!video || !source) return

    let cancelled = false
    let instance: JASSUB | null = null
    const controller = new AbortController()

    const load = async () => {
      try {
        const [response, converter] = await Promise.all([
          fetch(source, { signal: controller.signal }),
          loadSubtitleChineseConverter(chineseMode).catch(() => (text: string) => text),
        ])
        if (!response.ok) throw new Error(`subtitle request failed: ${response.status}`)
        const body = await response.text()
        if (!body.trim()) throw new Error('empty subtitle payload')
        if (cancelled) return

        instance = new JASSUB({
          video,
          subContent: convertASSContent(body, converter),
          timeOffset: timeOffsetRef.current,
          workerUrl: jassubWorkerUrl,
          wasmUrl: jassubWasmUrl,
          modernWasmUrl: jassubModernWasmUrl,
          fonts: [notoSansSCUrl],
          defaultFont: 'Noto Sans SC',
          queryFonts: 'local',
          prescaleFactor: 1,
        })
        instanceRef.current = instance
        await instance.ready
        if (cancelled) {
          void instance.destroy().catch(() => undefined)
          return
        }
        callbacksRef.current.onReady?.()
      } catch (error: unknown) {
        if (cancelled) return
        if (error instanceof DOMException && error.name === 'AbortError') return
        callbacksRef.current.onError?.(error)
      }
    }

    void load()

    return () => {
      cancelled = true
      controller.abort()
      instanceRef.current = null
      if (instance) void instance.destroy().catch(() => undefined)
    }
    // timeOffset is applied live below. Recreating the full libass worker on a
    // seek offset change would be unnecessarily expensive.
  }, [source, chineseMode, videoRef])

  useEffect(() => {
    timeOffsetRef.current = timeOffset
    if (instanceRef.current) instanceRef.current.timeOffset = timeOffset
  }, [timeOffset])

  return null
}
