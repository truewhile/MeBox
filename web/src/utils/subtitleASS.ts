export type SubtitleChineseConverter = (text: string) => string

function commaIndexAtField(line: string, fieldCount: number): number {
  let index = -1
  for (let field = 0; field < fieldCount; field += 1) {
    index = line.indexOf(',', index + 1)
    if (index < 0) return -1
  }
  return index
}

/**
 * Convert only the Text column of ASS/SSA Dialogue lines. ASS styling tags,
 * drawings, timing and positioning metadata are bytes-for-bytes preserved so
 * the event can still be rendered by libass after a Chinese conversion.
 */
export function convertASSContent(
  content: string,
  convert: SubtitleChineseConverter,
): string {
  return content
    .replace(/^\uFEFF/, '')
    .split(/\r?\n/)
    .map((line) => {
      const lower = line.toLowerCase()
      if (!lower.startsWith('dialogue:')) return line
      const textIndex = commaIndexAtField(line, 9)
      if (textIndex < 0) return line
      return `${line.slice(0, textIndex + 1)}${convert(line.slice(textIndex + 1))}`
    })
    .join('\n')
}
