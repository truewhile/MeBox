export const PLAYBACK_RATE_OPTIONS = [0.5, 0.75, 1, 1.25, 1.5, 1.75, 2, 2.5, 3] as const

export const MIN_PLAYBACK_RATE = PLAYBACK_RATE_OPTIONS[0]
export const MAX_PLAYBACK_RATE = PLAYBACK_RATE_OPTIONS[PLAYBACK_RATE_OPTIONS.length - 1]

export function normalizePlaybackRate(value: unknown): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 1
  let closest: number = PLAYBACK_RATE_OPTIONS[0]
  let closestDistance = Number.POSITIVE_INFINITY
  for (const option of PLAYBACK_RATE_OPTIONS) {
    const distance = Math.abs(option - parsed)
    if (distance < closestDistance) {
      closest = option
      closestDistance = distance
    }
  }
  return closest
}

export function stepPlaybackRate(current: unknown, direction: 1 | -1): number {
  const normalized = normalizePlaybackRate(current)
  const index = PLAYBACK_RATE_OPTIONS.indexOf(normalized as (typeof PLAYBACK_RATE_OPTIONS)[number])
  const next = Math.min(PLAYBACK_RATE_OPTIONS.length - 1, Math.max(0, index + direction))
  return PLAYBACK_RATE_OPTIONS[next]
}

export function formatPlaybackRate(rate: unknown): string {
  const normalized = normalizePlaybackRate(rate)
  return `${Number.isInteger(normalized) ? normalized.toFixed(0) : String(normalized)}x`
}
