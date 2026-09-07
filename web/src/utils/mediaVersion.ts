import type { Media } from '../types'

/** 从路径 / strm URL 推断容器扩展名（mkv/mp4…）。 */
export function mediaVersionContainer(media: Media): string {
  const path = (media.path || '').replace(/\\/g, '/')
  const base = path.split('/').pop() || ''
  const ext = base.includes('.') ? base.slice(base.lastIndexOf('.')).toLowerCase() : ''
  let name = ext ? base.slice(0, -ext.length) : base
  if (ext === '.strm') {
    const second = name.includes('.') ? name.slice(name.lastIndexOf('.')).toLowerCase() : ''
    if (second && second !== '.strm') {
      return second.replace(/^\./, '')
    }
    const strm = (media.strm_url || '').toLowerCase()
    const marker = '/video.'
    const idx = strm.lastIndexOf(marker)
    if (idx >= 0) {
      let rest = strm.slice(idx + marker.length)
      const end = rest.search(/[?#&/]/)
      if (end >= 0) rest = rest.slice(0, end)
      rest = rest.replace(/^\./, '').trim()
      if (rest) return rest
    }
    return 'strm'
  }
  return ext.replace(/^\./, '')
}

function formatSize(bytes: number): string {
  if (!bytes || bytes < 1024) return bytes ? `${bytes} B` : ''
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = -1
  do {
    value /= 1024
    unit += 1
  } while (value >= 1024 && unit < units.length - 1)
  return `${value.toFixed(1)} ${units[unit]}`
}

/** 版本切换展示名：分辨率 · 容器 · 编码 · 体积，缺省回退文件名。 */
export function mediaVersionLabel(media: Media): string {
  const parts: string[] = []
  if (media.height > 0) parts.push(`${media.height}p`)
  else if (media.width > 0) parts.push(`${media.width}w`)
  const container = mediaVersionContainer(media)
  if (container && container !== 'strm') parts.push(container.toUpperCase())
  if (media.video_codec?.trim()) parts.push(media.video_codec.trim().toUpperCase())
  const size = formatSize(media.size_bytes)
  if (size) parts.push(size)
  if (parts.length > 0) return parts.join(' · ')
  const base = (media.path || '').replace(/\\/g, '/').split('/').pop()
  return base || media.title || media.id
}

export function mediaVersionsOf(media: Media | null | undefined): Media[] {
  if (!media) return []
  if (media.versions && media.versions.length > 1) return media.versions
  return [media]
}
