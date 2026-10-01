export type SubtitleCueAlignment = 'start' | 'center' | 'end' | 'left' | 'right'
export type SubtitleCueVertical = 'rl' | 'lr'
export type SubtitleCuePositionAlignment = SubtitleCueAlignment | 'line-left' | 'line-right' | 'auto'

export type SubtitleCueSettings = {
  align?: SubtitleCueAlignment
  line?: string
  lineAlign?: SubtitleCueAlignment
  position?: number
  positionAlign?: SubtitleCuePositionAlignment
  size?: number
  vertical?: SubtitleCueVertical
}

export type SubtitleCue = {
  startTime: number
  endTime: number
  text: string
  settings: SubtitleCueSettings
}

function parseVTTTimestamp(value: string): number {
  const parts = value.trim().replace(',', '.').split(':')
  if (parts.length !== 2 && parts.length !== 3) return Number.NaN
  const seconds = Number(parts.pop())
  const minutes = Number(parts.pop())
  const hours = parts.length > 0 ? Number(parts.pop()) : 0
  if (![hours, minutes, seconds].every(Number.isFinite)) return Number.NaN
  return hours * 3600 + minutes * 60 + seconds
}

function parseCueSettings(tokens: string[]): SubtitleCueSettings {
  const settings: SubtitleCueSettings = {}
  for (const token of tokens) {
    const colon = token.indexOf(':')
    if (colon <= 0) continue
    const key = token.slice(0, colon).toLowerCase()
    const value = token.slice(colon + 1)
    if (key === 'align') {
      if (['start', 'center', 'end', 'left', 'right'].includes(value)) {
        settings.align = value as SubtitleCueAlignment
      }
    } else if (key === 'line') {
      settings.line = value
    } else if (key === 'linealign') {
      if (['start', 'center', 'end'].includes(value)) {
        settings.lineAlign = value as SubtitleCueAlignment
      }
    } else if (key === 'position') {
      const position = Number.parseFloat(value)
      if (Number.isFinite(position)) settings.position = Math.min(100, Math.max(0, position))
    } else if (key === 'positionalign') {
      if (['start', 'center', 'end', 'line-left', 'line-right', 'auto'].includes(value)) {
        settings.positionAlign = value as SubtitleCuePositionAlignment
      }
    } else if (key === 'size') {
      const size = Number.parseFloat(value)
      if (Number.isFinite(size)) settings.size = Math.min(100, Math.max(0, size))
    } else if (key === 'vertical') {
      if (value === 'rl' || value === 'lr') settings.vertical = value
    }
  }
  return settings
}

export function parseWebVTTCues(body: string): SubtitleCue[] {
  const blocks = body
    .replace(/^\uFEFF/, '')
    .replace(/\r\n?/g, '\n')
    .split(/\n{2,}/)
  const cues: SubtitleCue[] = []

  for (const block of blocks) {
    const lines = block.split('\n')
    const timingIndex = lines.findIndex((line) => line.includes('-->'))
    if (timingIndex < 0) continue

    const [rawStart, rawRest] = lines[timingIndex].split('-->', 2)
    const rest = rawRest.trim().split(/\s+/)
    const startTime = parseVTTTimestamp(rawStart)
    const endTime = parseVTTTimestamp(rest[0] || '')
    const text = lines.slice(timingIndex + 1).join('\n').trim()
    if (Number.isFinite(startTime) && Number.isFinite(endTime) && endTime >= startTime && text) {
      cues.push({
        startTime,
        endTime,
        text,
        settings: parseCueSettings(rest.slice(1)),
      })
    }
  }

  return cues
}
