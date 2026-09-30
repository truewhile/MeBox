import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// 阅读主题（色值对齐 legado assets/defaultData/readConfig.json 的 6 套内置主题，
// 含日/夜两态，见 docs/reader-ui-spec.md 第 2/10 节）。
export interface ReaderTheme {
  id: string
  name: string
  bg: string
  text: string
  accent: string
  nightBg: string
  nightText: string
  nightAccent: string
}

export const READER_THEMES: ReaderTheme[] = [
  { id: 'weread', name: '微信读书', bg: '#C0EDC6', text: '#0B0B0B', accent: '#E53935', nightBg: '#000000', nightText: '#ADADAD', nightAccent: '#FE4D55' },
  { id: 'preset1', name: '预设1', bg: '#FFFFFF', text: '#000000', accent: '#E53935', nightBg: '#000000', nightText: '#FFFFFF', nightAccent: '#FE4D55' },
  { id: 'preset2', name: '羊皮纸', bg: '#DDC090', text: '#3E3422', accent: '#834E00', nightBg: '#3C3F43', nightText: '#DCDFE1', nightAccent: '#FE4D55' },
  { id: 'preset3', name: '护眼绿', bg: '#C2D8AA', text: '#596C44', accent: '#E53935', nightBg: '#3C3F43', nightText: '#88C16F', nightAccent: '#FE4D55' },
  { id: 'preset4', name: '粉紫', bg: '#DBB8E2', text: '#68516C', accent: '#801314', nightBg: '#3C3F43', nightText: '#F6AEAE', nightAccent: '#90BFF5' },
  { id: 'preset5', name: '淡蓝', bg: '#ABCEE0', text: '#3D4C54', accent: '#E53935', nightBg: '#3C3F43', nightText: '#90BFF5', nightAccent: '#FE4D55' },
]

export function getReaderTheme(themeId: string, night: boolean): { bg: string; text: string; accent: string } {
  const t = READER_THEMES.find((x) => x.id === themeId) ?? READER_THEMES[1]
  return night
    ? { bg: t.nightBg, text: t.nightText, accent: t.nightAccent }
    : { bg: t.bg, text: t.text, accent: t.accent }
}

export type ReaderPageMode = 'page' | 'scroll'
export type ReaderHomeMode = 'media' | 'reading'

interface ReaderSettingsState {
  // 首页模式切换（影视 / 阅读）
  homeMode: ReaderHomeMode
  setHomeMode: (mode: ReaderHomeMode) => void

  themeId: string
  setThemeId: (id: string) => void

  night: boolean
  toggleNight: () => void

  pageMode: ReaderPageMode
  setPageMode: (mode: ReaderPageMode) => void

  fontSize: number
  setFontSize: (size: number) => void

  lineHeight: number
  setLineHeight: (lh: number) => void

  paragraphSpacing: number
  setParagraphSpacing: (v: number) => void
}

export const useReaderSettingsStore = create<ReaderSettingsState>()(
  persist(
    (set) => ({
      homeMode: 'media',
      setHomeMode: (mode) => set({ homeMode: mode }),

      themeId: 'preset1',
      setThemeId: (themeId) => set({ themeId }),

      night: false,
      toggleNight: () => set((s) => ({ night: !s.night })),

      pageMode: 'page',
      setPageMode: (pageMode) => set({ pageMode }),

      fontSize: 20,
      setFontSize: (fontSize) => set({ fontSize: Math.min(32, Math.max(14, fontSize)) }),

      lineHeight: 1.8,
      setLineHeight: (lineHeight) => set({ lineHeight: Math.min(2.6, Math.max(1.4, Math.round(lineHeight * 10) / 10)) }),

      paragraphSpacing: 8,
      setParagraphSpacing: (paragraphSpacing) =>
        set({ paragraphSpacing: Math.min(32, Math.max(0, paragraphSpacing)) }),
    }),
    { name: 'mebox-reader-settings' },
  ),
)
