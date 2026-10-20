import { create } from 'zustand'

import type { ReaderChapter } from '../api/reader'

// 听书全局播放状态（刻意不持久化）。
//
// 真正的播放器是模块级单例（见 pages/reader/readerAudioEngine.ts），React 组件只
// 读写这份状态、通过引擎的方法控制播放。这样音频不依附于任何路由：离开阅读页、
// 换到别的书、甚至回到影视首页，播放都继续，由 Media Session 提供锁屏/通知栏控制。
export type ReaderAudioStatus = 'idle' | 'loading' | 'playing' | 'paused' | 'error'

export interface ReaderAudioState {
  /** 当前音轨所属书籍；空串表示没有加载任何音轨。 */
  bookId: string
  bookName: string
  cover: string
  chapterIndex: number
  chapterTitle: string
  chapters: ReaderChapter[]
  /** 当前音轨地址（已签名，可直接交给 <audio> 或 hls.js）。 */
  track: string
  /** 该音轨由服务端转码，首次播放需要等待转码完成。 */
  transcoding: boolean
  openCredits: number
  closeCredits: number
  hasNext: boolean
  status: ReaderAudioStatus
  /** 加载/播放失败的可见原因（空串表示无错误）。 */
  error: string
  /** 当前播放位置（秒）。 */
  cur: number
  /** 总时长（秒），元数据未就绪时为 0。 */
  dur: number
  /** 播放倍速，与 readerSettings 的 audioSpeed 保持一致。 */
  speed: number
  timerActive: boolean
  /** 定时剩余秒数，0 表示未开启。 */
  timerLeft: number
  timerPreset: number
}

const initialState: ReaderAudioState = {
  bookId: '',
  bookName: '',
  cover: '',
  chapterIndex: 0,
  chapterTitle: '',
  chapters: [],
  track: '',
  transcoding: false,
  openCredits: 0,
  closeCredits: 0,
  hasNext: false,
  status: 'idle',
  error: '',
  cur: 0,
  dur: 0,
  speed: 1,
  timerActive: false,
  timerLeft: 0,
  timerPreset: 0,
}

export const useReaderAudioStore = create<ReaderAudioState>(() => ({ ...initialState }))

/** 引擎专用：整块重置回空闲态（停止播放时调用）。 */
export function resetReaderAudio(): void {
  useReaderAudioStore.setState({ ...initialState })
}
