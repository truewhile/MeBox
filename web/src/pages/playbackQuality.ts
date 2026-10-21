import type { PlaybackInfo, PlaybackQuality } from '../types/playback'

export function findPlaybackQualityById(info: PlaybackInfo | null, id: string): PlaybackQuality | undefined {
  if (!info || !id) return undefined
  return [...(info.cloud_qualities ?? []), ...(info.local_qualities ?? [])].find(
    (quality) => quality.id === id,
  )
}

export function defaultLocalQualityId(info: PlaybackInfo | null): string {
  const qualities = info?.local_qualities ?? []
  for (const id of ['1080', '720', '480', 'source']) {
    if (qualities.some((quality) => quality.id === id)) return id
  }
  return qualities[0]?.id ?? '720'
}

/** A cloud option may exist without being ready; it is never a local fallback. */
export function availableLocalQuality(info: PlaybackInfo | null, preferred = ''): PlaybackQuality | undefined {
  const available = info?.local_qualities.filter((quality) => quality.source === 'local' && quality.available) ?? []
  return available.find((quality) => quality.id === preferred) ??
    ['1080', '720', '480', 'source'].map((id) => available.find((quality) => quality.id === id)).find(Boolean) ??
    available[0]
}
