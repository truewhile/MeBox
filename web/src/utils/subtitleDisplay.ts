import type { CSSProperties } from 'react'

export type SubtitlePosition = 'auto' | 'bottom' | 'lower' | 'middle' | 'top'
export type SubtitleStylePreset = 'shadow' | 'outline' | 'box'

export const SUBTITLE_POSITION_OPTIONS: Array<[SubtitlePosition, string]> = [
  ['auto', '默认位置'],
  ['bottom', '贴底'],
  ['lower', '下方'],
  ['middle', '居中'],
  ['top', '顶部'],
]

export const SUBTITLE_STYLE_OPTIONS: Array<[SubtitleStylePreset, string]> = [
  ['shadow', '柔和阴影'],
  ['outline', '黑边描边'],
  ['box', '半透明底衬'],
]

const POSITION_KEY = 'mebox.subtitle.position'
const STYLE_KEY = 'mebox.subtitle.style'

export function normalizeSubtitlePosition(value: unknown): SubtitlePosition {
  return value === 'bottom' || value === 'lower' || value === 'middle' || value === 'top'
    ? value
    : 'auto'
}

export function normalizeSubtitleStyle(value: unknown): SubtitleStylePreset {
  return value === 'outline' || value === 'box' ? value : 'shadow'
}

export function loadSubtitlePosition(): SubtitlePosition {
  if (typeof window === 'undefined') return 'auto'
  try {
    return normalizeSubtitlePosition(window.localStorage.getItem(POSITION_KEY))
  } catch {
    return 'auto'
  }
}

export function loadSubtitleStyle(): SubtitleStylePreset {
  if (typeof window === 'undefined') return 'shadow'
  try {
    return normalizeSubtitleStyle(window.localStorage.getItem(STYLE_KEY))
  } catch {
    return 'shadow'
  }
}

export function saveSubtitlePosition(position: SubtitlePosition): void {
  try {
    window.localStorage.setItem(POSITION_KEY, position)
  } catch {
    // localStorage may be unavailable in private mode; the player still works.
  }
}

export function saveSubtitleStyle(style: SubtitleStylePreset): void {
  try {
    window.localStorage.setItem(STYLE_KEY, style)
  } catch {
    // localStorage may be unavailable in private mode; the player still works.
  }
}

export function subtitleTextStyle(style: SubtitleStylePreset): CSSProperties {
  if (style === 'outline') {
    return {
      fontWeight: 700,
      textShadow: [
        '-1.5px 0 #000',
        '1.5px 0 #000',
        '0 -1.5px #000',
        '0 1.5px #000',
        '-1px -1px #000',
        '1px -1px #000',
        '-1px 1px #000',
        '1px 1px #000',
      ].join(', '),
    }
  }
  if (style === 'box') {
    return {
      fontWeight: 600,
      background: 'rgba(0, 0, 0, 0.72)',
      padding: '0.16em 0.5em',
      borderRadius: '0.24em',
      boxDecorationBreak: 'clone',
      WebkitBoxDecorationBreak: 'clone',
      textShadow: '0 1px 2px rgba(0, 0, 0, 0.85)',
    }
  }
  return {
    fontWeight: 500,
    textShadow:
      '0 1px 3px rgba(0, 0, 0, 0.95), 0 0 8px rgba(0, 0, 0, 0.85), 0 0 16px rgba(0, 0, 0, 0.65)',
  }
}
