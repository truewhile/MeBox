import { Calendar } from 'lucide-react'

import type { Media } from '../types'

type MediaDetailMetadataProps = {
  media: Media
}

export function MediaDetailMetadata({ media }: MediaDetailMetadataProps) {
  const heading = media.episode_title?.trim() || media.title
  const showTitleContext = Boolean(media.episode_title?.trim() && media.title && media.title !== heading)
  const genres = parseCSV(media.genres)
  const languages = parseCSV(media.languages)
  const countries = parseCSV(media.countries)
  const hasTaxonomy = genres.length > 0 || languages.length > 0 || countries.length > 0

  return (
    <>
      <div className="space-y-3">
        <h1 className="font-display text-3xl sm:text-4xl font-extrabold tracking-tight text-gray-900 leading-tight">
          {heading}
        </h1>
        {showTitleContext && (
          <p className="text-sm font-semibold text-gray-500">
            {media.title}
          </p>
        )}
        <div className="flex flex-wrap items-center gap-2">
          {media.year > 0 && (
            <span className="inline-flex h-7 items-center rounded-lg border border-gray-200/60 bg-gray-100 px-2.5 text-2xs font-bold uppercase tracking-wide gap-1.5 text-gray-700">
              <Calendar size={13} className="text-brand-500" />
              <span>{media.year} 年</span>
            </span>
          )}
          {media.width > 0 && media.height > 0 && (
            <span className="inline-flex h-7 items-center rounded-lg border border-brand-100/50 bg-brand-50 px-2.5 text-2xs font-bold uppercase tracking-wide text-brand-700">
              <span>{media.width} × {media.height}</span>
            </span>
          )}
          {media.size_bytes > 0 && (
            <span className="inline-flex h-7 items-center rounded-lg border border-gray-200/60 bg-gray-100 px-2.5 text-2xs font-bold uppercase tracking-wide text-gray-700">{fmtSize(media.size_bytes)}</span>
          )}
          {media.duration_sec > 0 && (
            <span className="inline-flex h-7 items-center rounded-lg border border-gray-200/60 bg-gray-100 px-2.5 text-2xs font-bold uppercase tracking-wide text-gray-700">{fmtDuration(media.duration_sec)}</span>
          )}
          {media.container && (
            <span className="inline-flex h-7 items-center rounded-lg border border-gray-200/60 bg-gray-100 px-2.5 text-2xs font-bold uppercase tracking-wide text-gray-700 font-mono">
              {media.container}
            </span>
          )}
        </div>
      </div>

      {media.overview && (
        <div className="rounded-2xl bg-gray-50/50 border border-gray-100 p-5 space-y-2">
          <h3 className="text-xs font-bold uppercase tracking-widest text-brand-500">剧情简介</h3>
          <p className="text-sm text-gray-600 leading-relaxed font-semibold">
            {media.overview}
          </p>
        </div>
      )}

      {hasTaxonomy && (
        <div className="space-y-3">
          <MetadataRow label="类型流派" values={parseCSV(media.genres)} tone="brand" />
          <MetadataRow label="语言" values={parseCSV(media.languages)} />
          <MetadataRow label="国家/地区" values={parseCSV(media.countries)} />
          </div>
      )}
    </>
  )
}

/**
 * One "label - values" row of the taxonomy block.
 *
 * All rows share the same label gutter, so every value column starts on the
 * same vertical line. Pills keep one geometry and differ by tone only.
 */
function MetadataRow({
  label,
  values,
  tone = 'neutral',
}: {
  label: string
  values: string[]
  tone?: 'neutral' | 'brand'
}) {
  if (values.length === 0) return null
  const tagClass = [
    'inline-flex items-center rounded-lg border px-2.5 py-1 text-2xs font-bold uppercase tracking-wide',
    tone === 'brand'
      ? 'border-brand-100/50 bg-brand-50 text-brand-700'
      : 'border-gray-200/60 bg-gray-100 text-gray-600',
  ].join(' ')

  return (
    <div className="grid grid-cols-[4.5rem_1fr] items-start gap-2 sm:grid-cols-[5.5rem_1fr]">
      <span className="pt-1 text-xs font-semibold text-gray-500">{label}</span>
      <div className="flex flex-wrap gap-1.5">
        {values.map((value) => (
          <span key={value} className={tagClass}>
            {value}
          </span>
        ))}
      </div>
    </div>
  )
}

function fmtDuration(sec: number): string {
  if (!sec || sec <= 0) return '—'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  return h > 0 ? `${h}h ${m}m` : `${m}m`
}

function fmtSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(2)} ${units[i]}`
}

function parseCSV(s?: string): string[] {
  if (!s) return []
  return s.split(',').map((x) => x.trim()).filter(Boolean)
}
