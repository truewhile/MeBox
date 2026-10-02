// 听书播放引擎：模块级单例，持有真正的 <audio>（不进 React 树，也不进任何路由）。
//
// 为什么要脱离组件：音频面板原先挂在 ReaderViewPage 里，路由一变组件卸载，清理
// 逻辑就 removeAttribute('src') + load()，播放当场断掉；手机息屏、系统回收标签页
// 时更没有任何东西证明「这个页面正在播媒体」。把播放器做成常驻单例后：
//   - 路由切换不影响播放；
//   - Media Session 让锁屏/通知栏/耳机按键接管控制（Android Chrome 会据此判定
//     页面处于活跃媒体会话，后台存活率明显更高）；
//   - 定时关闭改由 timeupdate（后台播放时仍触发）+ 低频 ticker 驱动，不再依赖
//     被节流的 setInterval 来「到点暂停」。
//
// 进度上报也在这里：引擎自己知道当前书/章，离开页面、暂停、切章时都能补报一次，
// 不依赖调用方在卸载时回调。
import toast from 'react-hot-toast'

import { readerAPI, type ReaderChapter } from '../../api/reader'
import { useReaderAudioStore, resetReaderAudio } from '../../stores/readerAudio'
import { useReaderSettingsStore } from '../../stores/readerSettings'
import { readerCoverSrc } from '../../utils/readerCover'

/** 播放进度上报节流（毫秒）：退出最多丢 5 秒。 */
const PROGRESS_SAVE_INTERVAL_MS = 5_000
/** -15s / +15s 步长（对齐面板与 legado SEEK_STEP）。 */
const SEEK_STEP = 15

export interface LoadChapterInput {
  bookId: string
  bookName: string
  cover: string
  chapters: ReaderChapter[]
  chapterIndex: number
  /** 当前章的音轨地址（已签名）。 */
  track: string
  transcoding: boolean
  openCredits: number
  closeCredits: number
  /** 恢复进度（秒）；0 表示从头。 */
  initialPos: number
}

class ReaderAudioEngine {
  private audio: HTMLAudioElement
  /** hls.js 实例（只用到 destroy）。 */
  private hls: { destroy: () => void } | null = null
  /** 元数据就绪后是否已经套用过进度/片头跳转。 */
  private restored = false
  /** 本章是否已经触发过片尾跳过，避免 timeupdate 反复触发。 */
  private skippedEnd = false
  /** 最近一次播放位置（秒）：节流窗口外补报进度时要用。 */
  private lastTime = 0
  private lastSavedAt = 0
  /** 上一次落库的进度标识（书|章|秒），相同则不重复请求。 */
  private savedKey = ''
  /** 定时关闭截止时间戳（毫秒），0 表示未开启。 */
  private timerDeadline = 0
  /** 暂停开始的毫秒时间戳，0 表示当前在播（定时倒计时只在播放中走）。 */
  private pausedAt = 0
  private timerTicker: number | null = null
  /** 一次性的初始进度，等 metadata 就绪后消费。 */
  private pendingInitialPos = 0
  /** 是否已经套用过「上次的定时设置」，只在首次加载时套一次。 */
  private timerBooted = false
  /** 正在拆卸音源（避免拆卸触发的 pause 事件被当成用户暂停）。 */
  private detaching = false
  /** 加载批次号：动态 import hls.js 期间被新的加载打断时用来丢弃旧结果。 */
  private loadToken = 0

  constructor() {
    const audio = new Audio()
    audio.preload = 'metadata'
    this.audio = audio
    this.bindAudio()
    this.bindLifecycle()
    this.bindSettings()
    this.bindMediaSession()
  }

  // ─── 对外 API ─────────────────────────────────────────────────────────

  /**
   * 加载并播放一章。同一本书同一章重复调用（阅读页 effect 重跑、目录同步回写）
   * 会被忽略，避免把正在播的进度重置掉。
   */
  loadChapter(input: LoadChapterInput): void {
    const s = useReaderAudioStore.getState()
    if (
      s.bookId === input.bookId &&
      s.chapterIndex === input.chapterIndex &&
      s.track === input.track &&
      (s.status === 'loading' || s.status === 'playing' || s.status === 'paused')
    ) {
      return
    }
    // 换章前把上一章的最后位置补报掉（此时 store 里还是上一章的上下文）
    this.commitProgress()

    const chapterTitle = input.chapters[input.chapterIndex]?.title ?? ''
    useReaderAudioStore.setState({
      bookId: input.bookId,
      bookName: input.bookName,
      cover: input.cover,
      chapters: input.chapters,
      chapterIndex: input.chapterIndex,
      chapterTitle,
      track: input.track,
      transcoding: input.transcoding,
      openCredits: input.openCredits,
      closeCredits: input.closeCredits,
      hasNext: this.computeHasNext(input.chapters, input.chapterIndex),
      status: 'loading',
      error: '',
      cur: 0,
      dur: 0,
    })

    this.restored = false
    this.skippedEnd = false
    this.lastTime = 0
    this.lastSavedAt = 0
    this.savedKey = ''
    this.pendingInitialPos = Math.max(0, input.initialPos)

    // 进场套用上次的定时设置（对应 legado 起朗读服务时 setTimer(AppConfig.ttsTimer)）
    if (!this.timerBooted) {
      this.timerBooted = true
      const preset = useReaderSettingsStore.getState().audioTimerMinutes
      if (preset > 0) this.applyTimer(preset)
    }

    void this.attachSource(input.track)
  }

  play(): void {
    void this.audio.play().catch(() => {
      // 自动播放被浏览器拦下（典型是 iOS）：退回暂停态，让用户点按钮手动触发，
      // 而不是一直停在「加载中」。
      if (useReaderAudioStore.getState().status === 'loading') {
        useReaderAudioStore.setState({ status: 'paused' })
      }
    })
  }

  pause(): void {
    this.audio.pause()
    // 还没触发 play 事件时（status 仍是 loading），下面的 pause 事件处理会跳过状态
    // 判定；这里补一次，保证按钮不会一直转圈。
    if (useReaderAudioStore.getState().status === 'loading') {
      if (this.pausedAt === 0) this.pausedAt = Date.now()
      useReaderAudioStore.setState({ status: 'paused' })
      this.setSessionPlaybackState('paused')
    }
  }

  toggle(): void {
    if (this.audio.paused) this.play()
    else this.pause()
  }

  seekBy(delta: number): void {
    const total = Number.isFinite(this.audio.duration)
      ? this.audio.duration
      : this.audio.currentTime + Math.abs(delta)
    this.seekTo(Math.min(Math.max(0, this.audio.currentTime + delta), Math.max(0, total - 0.2)))
  }

  seekTo(seconds: number): void {
    if (!Number.isFinite(seconds)) return
    this.audio.currentTime = Math.max(0, seconds)
    this.lastTime = this.audio.currentTime
    useReaderAudioStore.setState({ cur: this.audio.currentTime })
    this.updateSessionPosition()
  }

  /** 倍速改动写回 settings store，订阅回调负责应用到 <audio>。 */
  setSpeed(v: number): void {
    useReaderSettingsStore.getState().setAudioSpeed(v)
  }

  /** 片头/片尾跳过秒数：立刻生效并按书持久化。 */
  setCredits(open: number, close: number): void {
    useReaderAudioStore.setState({ openCredits: open, closeCredits: close })
    const { bookId } = useReaderAudioStore.getState()
    if (!bookId) return
    readerAPI.saveAudioConfig(bookId, { open_credits: open, close_credits: close }).catch(() => undefined)
  }

  /** 定时关闭：0 表示关闭定时。 */
  applyTimer(minutes: number): void {
    useReaderSettingsStore.getState().setAudioTimerMinutes(minutes)
    useReaderAudioStore.setState({ timerPreset: minutes })
    if (minutes <= 0) {
      this.timerDeadline = 0
      this.pausedAt = 0
      this.stopTimerTicker()
      useReaderAudioStore.setState({ timerLeft: 0, timerActive: false })
      return
    }
    this.timerDeadline = Date.now() + minutes * 60_000
    // 暂停期间不倒计时：记下暂停起点，恢复播放时顺延截止时间
    this.pausedAt = this.audio.paused ? Date.now() : 0
    useReaderAudioStore.setState({ timerLeft: minutes * 60, timerActive: true })
    this.startTimerTicker()
  }

  selectChapter(index: number): void {
    void this.goToChapter(index, 0)
  }

  next(): void {
    const i = this.neighborChapterIndex(1)
    if (i === null) return
    void this.goToChapter(i, 0)
  }

  prev(): void {
    const i = this.neighborChapterIndex(-1)
    if (i === null) return
    void this.goToChapter(i, 0)
  }

  /** 完全停止并清空状态（迷你播放器的关闭按钮）。 */
  stop(): void {
    this.commitProgress()
    this.detachSource()
    this.timerDeadline = 0
    this.pausedAt = 0
    // 下一次开播重新套用「上次的定时设置」，否则关掉再听一本书时定时不会自动开
    this.timerBooted = false
    this.stopTimerTicker()
    this.setSessionMetadata(null)
    this.setSessionPlaybackState('none')
    resetReaderAudio()
  }

  /** 立即补报一次进度（不吃节流窗口）。 */
  commitProgress(): void {
    this.lastSavedAt = 0
    this.persistProgress()
  }

  // ─── 音源装载 ─────────────────────────────────────────────────────────

  private async attachSource(src: string): Promise<void> {
    const token = ++this.loadToken
    this.detachSource()
    this.setSessionMetadata(src)
    if (!src) {
      this.fail('本章没有可播放的音频')
      return
    }
    if (src.includes('.m3u8')) {
      const mod = await import('hls.js')
      // 动态 import 期间可能已经切到别的章：丢弃这次结果
      if (token !== this.loadToken) return
      const Hls = mod.default
      if (Hls.isSupported()) {
        const inst = new Hls({ enableWorker: true })
        inst.on(Hls.Events.ERROR, (_evt, data) => {
          if (data.fatal) this.fail('音频加载失败，请检查网络或源文件是否还在')
        })
        inst.loadSource(src)
        inst.attachMedia(this.audio)
        this.hls = inst
        return
      }
      // Safari 原生 HLS
      this.audio.src = src
      return
    }
    this.audio.src = src
  }

  private detachSource(): void {
    this.detaching = true
    this.hls?.destroy()
    this.hls = null
    this.audio.removeAttribute('src')
    this.audio.load()
    this.detaching = false
  }

  private async goToChapter(index: number, initialPos: number): Promise<void> {
    const { bookId, chapters } = useReaderAudioStore.getState()
    if (!bookId || index < 0 || index >= chapters.length || chapters[index].is_volume) return
    useReaderAudioStore.setState({ status: 'loading', error: '' })
    try {
      const ct = await readerAPI.bookContent(bookId, index)
      const track = ct.tracks?.[0]
      if (ct.type !== 'audio' || !track) {
        this.fail('本章没有可播放的音频')
        return
      }
      const s = useReaderAudioStore.getState()
      this.loadChapter({
        bookId,
        bookName: s.bookName,
        cover: s.cover,
        chapters,
        chapterIndex: index,
        track,
        transcoding: ct.transcoding ?? false,
        openCredits: s.openCredits,
        closeCredits: s.closeCredits,
        initialPos,
      })
    } catch (e) {
      this.fail((e as Error).message || '章节加载失败')
    }
  }

  private fail(message: string): void {
    useReaderAudioStore.setState({ status: 'error', error: message })
    this.setSessionPlaybackState('paused')
  }

  // ─── <audio> 事件 ─────────────────────────────────────────────────────

  private bindAudio(): void {
    const audio = this.audio

    audio.addEventListener('play', () => {
      useReaderAudioStore.setState({ status: 'playing' })
      this.setSessionPlaybackState('playing')
      // 恢复播放：把暂停时长顺延到定时截止时间
      if (this.pausedAt > 0) {
        this.timerDeadline += Date.now() - this.pausedAt
        this.pausedAt = 0
      }
    })

    audio.addEventListener('pause', () => {
      if (this.detaching) return
      // 只认「播放中 -> 暂停」这一种。换源/停止时 load() 也会抛出 pause 事件，
      // 那些时刻 status 是 loading/idle，直接忽略；否则会把刚进入的加载态改成暂停，
      // 并给新章节写一条位置为 0 的进度。
      if (useReaderAudioStore.getState().status !== 'playing') return
      if (this.pausedAt === 0) this.pausedAt = Date.now()
      useReaderAudioStore.setState({ status: 'paused' })
      this.setSessionPlaybackState('paused')
      this.commitProgress()
    })

    audio.addEventListener('ended', () => {
      this.lastTime = Number.isFinite(audio.duration) ? audio.duration : this.lastTime
      this.commitProgress()
      if (useReaderAudioStore.getState().hasNext) {
        this.next()
        return
      }
      // 最后一章播完：ended 不会触发 pause，倒计时/暂停态要在这里自己落定
      if (this.pausedAt === 0) this.pausedAt = Date.now()
      useReaderAudioStore.setState({ status: 'paused' })
      this.setSessionPlaybackState('paused')
    })

    audio.addEventListener('loadedmetadata', () => {
      const total = Number.isFinite(audio.duration) ? audio.duration : 0
      useReaderAudioStore.setState({ dur: total })
      if (!this.restored) {
        const { openCredits } = useReaderAudioStore.getState()
        if (this.pendingInitialPos > 0 && (total === 0 || this.pendingInitialPos < total)) {
          // 有进度：按存档续播（legado: position != 0 时不套用片头）
          audio.currentTime = this.pendingInitialPos
          useReaderAudioStore.setState({ cur: this.pendingInitialPos })
        } else if (openCredits > 0 && (total === 0 || total > openCredits + 1)) {
          // 全新开播：跳到片头结束位置（legado: skipStartMs）
          audio.currentTime = openCredits
          useReaderAudioStore.setState({ cur: openCredits })
        }
        this.pendingInitialPos = 0
      }
      this.restored = true
      audio.playbackRate = useReaderAudioStore.getState().speed
      this.play()
    })

    audio.addEventListener('durationchange', () => {
      const total = Number.isFinite(audio.duration) ? audio.duration : 0
      useReaderAudioStore.setState({ dur: total })
      this.updateSessionPosition()
    })

    audio.addEventListener('timeupdate', () => {
      const t = audio.currentTime
      this.lastTime = t
      useReaderAudioStore.setState({ cur: t })
      this.updateSessionPosition()
      this.reportProgress()
      this.checkCreditsEnd()
      // 后台播放时 timeupdate 仍会触发，这是「到点暂停」最可靠的位置
      this.checkTimer()
    })

    audio.addEventListener('error', () => {
      const code = audio.error?.code
      this.fail(
        code === MediaError.MEDIA_ERR_SRC_NOT_SUPPORTED
          ? '浏览器无法播放该音频格式，且服务端转码不可用（请检查 ffmpeg 是否已安装）'
          : code === MediaError.MEDIA_ERR_NETWORK
            ? '音频加载失败，请检查网络或源文件是否还在'
            : '音频无法播放',
      )
    })
  }

  // ─── 页面生命周期：只负责补报进度，绝不暂停播放 ───────────────────────

  private bindLifecycle(): void {
    const flush = () => this.commitProgress()
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'hidden') flush()
    })
    window.addEventListener('pagehide', flush)
  }

  private bindSettings(): void {
    const apply = () => {
      const { audioSpeed } = useReaderSettingsStore.getState()
      this.audio.playbackRate = audioSpeed
      useReaderAudioStore.setState({ speed: audioSpeed })
    }
    apply()
    // 引擎是常驻单例，订阅不需要解除
    useReaderSettingsStore.subscribe(apply)
  }

  // ─── 进度 ─────────────────────────────────────────────────────────────

  private reportProgress(): void {
    const now = Date.now()
    if (now - this.lastSavedAt < PROGRESS_SAVE_INTERVAL_MS) return
    this.lastSavedAt = now
    this.persistProgress()
  }

  private persistProgress(): void {
    const { bookId, chapterIndex, chapterTitle } = useReaderAudioStore.getState()
    if (!bookId || this.lastTime <= 0) return
    const pos = Math.max(0, Math.floor(this.lastTime))
    const key = `${bookId}|${chapterIndex}|${pos}`
    if (key === this.savedKey) return
    this.savedKey = key
    readerAPI.saveProgress(bookId, { chapter_index: chapterIndex, pos, chapter_title: chapterTitle }).catch(() => undefined)
  }

  // ─── 片头片尾 ─────────────────────────────────────────────────────────

  private checkCreditsEnd(): void {
    const { closeCredits, hasNext } = useReaderAudioStore.getState()
    const audio = this.audio
    if (closeCredits <= 0 || this.skippedEnd) return
    if (!Number.isFinite(audio.duration) || audio.duration <= closeCredits + 1) return
    if (audio.currentTime < audio.duration - closeCredits) return
    this.skippedEnd = true
    if (!hasNext) {
      audio.pause()
      audio.currentTime = Math.max(0, audio.duration - 0.5)
      return
    }
    this.next()
  }

  // ─── 定时关闭 ─────────────────────────────────────────────────────────

  /** 剩余秒数：暂停期间以暂停起点为准，倒计时不会在暂停时溜走。 */
  private timerLeftSeconds(): number {
    if (this.timerDeadline === 0) return 0
    const now = this.pausedAt > 0 ? this.pausedAt : Date.now()
    return Math.max(0, Math.round((this.timerDeadline - now) / 1000))
  }

  private checkTimer(): void {
    const s = useReaderAudioStore.getState()
    if (!s.timerActive || this.timerDeadline === 0) return
    const left = this.timerLeftSeconds()
    if (left !== s.timerLeft) useReaderAudioStore.setState({ timerLeft: left })
    // 暂停中不判到期：恢复播放时截止时间已经顺延过
    if (left <= 0 && this.pausedAt === 0) {
      this.timerDeadline = 0
      this.stopTimerTicker()
      useReaderAudioStore.setState({ timerLeft: 0, timerActive: false })
      this.audio.pause()
      toast('定时结束，已暂停播放')
    }
  }

  private startTimerTicker(): void {
    this.stopTimerTicker()
    this.timerTicker = window.setInterval(() => this.checkTimer(), 1000)
  }

  private stopTimerTicker(): void {
    if (this.timerTicker !== null) {
      window.clearInterval(this.timerTicker)
      this.timerTicker = null
    }
  }

  // ─── Media Session（锁屏 / 通知栏 / 耳机按键） ────────────────────────

  private session(): MediaSession | null {
    if (typeof navigator === 'undefined' || !('mediaSession' in navigator)) return null
    return navigator.mediaSession
  }

  private setSessionMetadata(track: string | null): void {
    const session = this.session()
    if (!session || typeof MediaMetadata === 'undefined') return
    if (track === null) {
      session.metadata = null
      return
    }
    const { bookName, chapterTitle, cover } = useReaderAudioStore.getState()
    const coverSrc = readerCoverSrc(cover)
    const artwork: MediaImage[] = coverSrc
      ? [{ src: new URL(coverSrc, window.location.origin).href, sizes: '512x512' }]
      : []
    try {
      session.metadata = new MediaMetadata({
        title: chapterTitle || bookName || 'MeBox 听书',
        artist: bookName || 'MeBox',
        album: 'MeBox 听书',
        artwork,
      })
    } catch {
      // 个别浏览器对 artwork 类型挑剔，失败不影响播放
    }
  }

  private setSessionPlaybackState(state: MediaSessionPlaybackState): void {
    const session = this.session()
    if (!session) return
    try {
      session.playbackState = state
    } catch {
      // 忽略
    }
  }

  private updateSessionPosition(): void {
    const session = this.session()
    if (!session || typeof session.setPositionState !== 'function') return
    const { dur } = useReaderAudioStore.getState()
    const position = this.audio.currentTime
    if (!Number.isFinite(dur) || dur <= 0 || !Number.isFinite(position)) return
    const rate = this.audio.playbackRate
    try {
      session.setPositionState({
        duration: dur,
        position: Math.min(Math.max(0, position), dur),
        playbackRate: Number.isFinite(rate) && rate > 0 ? rate : 1,
      })
    } catch {
      // position 越界等情况下浏览器会抛错，忽略
    }
  }

  private bindMediaSession(): void {
    const session = this.session()
    if (!session) return
    const handlers: [MediaSessionAction, MediaSessionActionHandler][] = [
      ['play', () => this.play()],
      ['pause', () => this.pause()],
      ['stop', () => this.stop()],
      ['seekbackward', () => this.seekBy(-SEEK_STEP)],
      ['seekforward', () => this.seekBy(SEEK_STEP)],
      ['previoustrack', () => this.prev()],
      ['nexttrack', () => this.next()],
      [
        'seekto',
        (details) => {
          if (typeof details.seekTime === 'number') this.seekTo(details.seekTime)
        },
      ],
    ]
    for (const [action, handler] of handlers) {
      try {
        session.setActionHandler(action, handler)
      } catch {
        // 该动作在当前浏览器不支持时会抛错，逐个忽略
      }
    }
  }

  // ─── 工具 ─────────────────────────────────────────────────────────────

  private neighborChapterIndex(dir: 1 | -1): number | null {
    const { chapters, chapterIndex } = useReaderAudioStore.getState()
    for (let i = chapterIndex + dir; i >= 0 && i < chapters.length; i += dir) {
      if (!chapters[i].is_volume) return i
    }
    return null
  }

  private computeHasNext(chapters: ReaderChapter[], index: number): boolean {
    for (let i = index + 1; i < chapters.length; i++) {
      if (!chapters[i].is_volume) return true
    }
    return false
  }
}

let engine: ReaderAudioEngine | null = null

/**
 * 取全局听书引擎（首次调用时才构造，避免没听过书的应用也创建一个 <audio>）。
 */
export function getReaderAudioEngine(): ReaderAudioEngine {
  if (!engine) engine = new ReaderAudioEngine()
  return engine
}
