import type { Media } from '../types'

const VIDEO_EXTENSIONS = new Set([
  'mkv', 'mp4', 'm4v', 'avi', 'mov', 'webm', 'flv', 'wmv', 'ts', 'm2ts', 'mts', 'vob', 'rmvb', 'rm', 'iso',
])

export function isStrmMedia(media?: Media | null): boolean {
  if (!media) return false
  const path = (media.path || '').toLowerCase()
  const strmUrl = (media.strm_url || '').toLowerCase()
  return path.endsWith('.strm') || path.startsWith('cloud://') || Boolean(strmUrl.trim())
}

/** 从路径 / strm URL 推断真实视频容器扩展名（mkv/mp4…）。 */
export function mediaVersionContainer(media: Media): string {
  const explicit = (media.container || '').trim().toLowerCase().replace(/^\./, '')
  if (explicit && explicit !== 'strm' && VIDEO_EXTENSIONS.has(explicit)) {
    return explicit
  }

  const path = (media.path || '').replace(/\\/g, '/')
  const base = path.split('/').pop() || ''
  const ext = base.includes('.') ? base.slice(base.lastIndexOf('.')).toLowerCase() : ''
  const name = ext ? base.slice(0, -ext.length) : base

  if (ext === '.strm') {
    const second = name.includes('.') ? name.slice(name.lastIndexOf('.')).toLowerCase().replace(/^\./, '') : ''
    if (second && VIDEO_EXTENSIONS.has(second)) {
      return second
    }

    const strm = (media.strm_url || '').toLowerCase()
    if (strm) {
      // 1. /video.mkv 或 /video.mp4 模式
      const marker = '/video.'
      const idx = strm.lastIndexOf(marker)
      if (idx >= 0) {
        let rest = strm.slice(idx + marker.length)
        const end = rest.search(/[?#&/]/)
        if (end >= 0) rest = rest.slice(0, end)
        rest = rest.replace(/^\./, '').trim()
        if (rest && VIDEO_EXTENSIONS.has(rest)) return rest
      }

      // 2. 从 query 参数（如 ref=/movies/abc.mkv 或 path=...）或 pathname 里匹配扩展名
      const match = strm.match(/\.(mkv|mp4|m4v|avi|mov|webm|flv|wmv|ts|m2ts|iso)(?=[?#&/]|$)/i)
      if (match?.[1]) {
        return match[1].toLowerCase()
      }
    }
    return 'strm'
  }

  const cleanExt = ext.replace(/^\./, '')
  return VIDEO_EXTENSIONS.has(cleanExt) ? cleanExt : cleanExt
}

function formatSize(bytes: number): string {
  if (!bytes || bytes < 1024) return ''
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = -1
  do {
    value /= 1024
    unit += 1
  } while (value >= 1024 && unit < units.length - 1)
  return `${value.toFixed(1)} ${units[unit]}`
}

/** 从文件名/路径中提取质量或规格标签（如 4K, 2160p, REMUX, HDR, 1080p, 60fps 等） */
function extractQualityKeywords(text: string): string[] {
  const result: string[] = []
  const upper = text.toUpperCase()

  // 分辨率
  if (/\b(4K|2160P|UHD)\b/i.test(upper)) result.push('4K')
  else if (/\b(1080P|FHD)\b/i.test(upper)) result.push('1080p')
  else if (/\b(720P|HD)\b/i.test(upper)) result.push('720p')

  // 格式/版本规格
  if (/\bREMUX\b/i.test(upper)) result.push('REMUX')
  else if (/\b(BLURAY|BLU-RAY|BD)\b/i.test(upper)) result.push('BluRay')
  else if (/\b(WEB-?DL|WEBRIP)\b/i.test(upper)) result.push('WEB-DL')

  // 动态范围/画质技术
  if (/\b(DV|DOVI|DOLBY[\s._-]?VISION)\b/i.test(upper)) result.push('DV')
  if (/\b(HDR10\+|HDR10PLUS)\b/i.test(upper)) result.push('HDR10+')
  else if (/\bHDR\b/i.test(upper) && !result.includes('DV')) result.push('HDR')

  // 帧率
  if (/\b(60FPS|120FPS)\b/i.test(upper)) {
    const fpsMatch = upper.match(/\b(60FPS|120FPS)\b/i)
    if (fpsMatch) result.push(fpsMatch[1])
  }

  return result
}

/** 清理文件名以用作干净的兜底展示 */
function cleanFallbackFilename(raw: string): string {
  let name = raw.replace(/\\/g, '/').split('/').pop() || ''
  // 去除 .strm 及前缀扩展名
  name = name.replace(/\.strm$/i, '')
  name = name.replace(/\.(mkv|mp4|m4v|avi|mov|webm|flv|wmv|ts|iso)$/i, '')
  // 替换连续的点或下划线为空格
  name = name.replace(/[._]+/g, ' ').trim()
  return name
}

/** 版本切换展示名：分辨率 · 容器 · 编码 · 体积 / 规格 / 来源标签。 */
export function mediaVersionLabel(media: Media): string {
  const isStrm = isStrmMedia(media)
  const parts: string[] = []

  // 1. 分辨率（优先从媒体元数据获取，其次从路径中提取）
  if (media.height > 0) {
    if (media.height >= 2100) parts.push('4K')
    else parts.push(`${media.height}p`)
  } else if (media.width > 0) {
    if (media.width >= 3800) parts.push('4K')
    else parts.push(`${media.width}w`)
  }

  // 2. 容器
  const container = mediaVersionContainer(media)
  if (container && container !== 'strm') {
    parts.push(container.toUpperCase())
  }

  // 3. 视频编码
  if (media.video_codec?.trim()) {
    parts.push(media.video_codec.trim().toUpperCase())
  }

  // 4. 从文件名/路径补充规格特征（针对 STRM 缺乏探测数据时的关键画质提示）
  const pathKeywords = extractQualityKeywords(`${media.path || ''} ${media.strm_url || ''}`)
  for (const kw of pathKeywords) {
    // 避免重复（如已经有了 4K / 1080p）
    if (!parts.some((p) => p.toUpperCase() === kw.toUpperCase())) {
      parts.push(kw)
    }
  }

  // 5. 文件大小：过滤掉本地 STRM 占位小文件（< 1MB 的 STRM 索引通常不是真实视频大小）
  const isPlaceholderSize = isStrm && media.size_bytes > 0 && media.size_bytes < 1024 * 1024
  if (media.size_bytes > 0 && !isPlaceholderSize) {
    const size = formatSize(media.size_bytes)
    if (size) parts.push(size)
  }

  // 6. 如果仍然没有解析出足够特征，标记来源（云盘 / STRM）
  if (isStrm && !parts.some((p) => ['4K', '1080P', '720P', 'REMUX', 'WEB-DL', 'MKV', 'MP4'].includes(p.toUpperCase()))) {
    parts.push('云端直链')
  }

  if (parts.length > 0) {
    return parts.join(' · ')
  }

  // 兜底：使用清理后的文件名
  const cleaned = cleanFallbackFilename(media.path || '')
  return cleaned || media.title || media.id
}

export function mediaVersionsOf(media: Media | null | undefined): Media[] {
  if (!media) return []
  if (media.versions && media.versions.length > 1) return media.versions
  return [media]
}
