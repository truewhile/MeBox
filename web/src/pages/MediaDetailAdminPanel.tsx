import { Database, FileText, FolderInput, Pencil, Search, ShieldAlert, Sparkles, Trash2 } from 'lucide-react'

import { EpisodeArtworkToggle } from '../components/EpisodeArtworkToggle'
import type { Media } from '../types'
import { isRemoteEmbyID } from '../utils/remoteEmby'

type MediaDetailAdminPanelProps = {
  media: Media
  scrapeEpisodeArtwork: boolean
  onScrapeEpisodeArtworkChange: (checked: boolean) => void
  onSmartScrape: () => void
  onManualScrape: () => void
  onMetadataEdit: () => void
  onOrganize: () => void
  onProbe: () => void
  onExportNFO: () => void
  onDelete: () => void
}

export function MediaDetailAdminPanel({
  media,
  scrapeEpisodeArtwork,
  onScrapeEpisodeArtworkChange,
  onSmartScrape,
  onManualScrape,
  onMetadataEdit,
  onOrganize,
  onProbe,
  onExportNFO,
  onDelete,
}: MediaDetailAdminPanelProps) {
  if (isRemoteEmbyID(media.id)) {
    return null
  }

  return (
    <section className="overflow-hidden rounded-2xl border border-gray-200 bg-gray-50/50">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200/60 px-4 py-3 sm:px-5">
        <p className="inline-flex items-center gap-2 text-2xs font-bold uppercase tracking-[0.18em] text-gray-500">
          <ShieldAlert size={14} className="text-brand-500" />
          系统后台高级控制面板
        </p>
        {isEpisodeArtworkTarget(media) && (
          <EpisodeArtworkToggle
            checked={scrapeEpisodeArtwork}
            onChange={onScrapeEpisodeArtworkChange}
            title="关闭后仍会获取每集简介、评分和时长，只跳过单集图片"
            className="h-9 text-xs"
          />
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2 px-4 py-4 sm:px-5">
        <button onClick={onSmartScrape} className="btn-outline h-9 rounded-lg px-3 text-xs gap-1.5 border-gray-200 hover:border-brand-500/50 hover:bg-brand-50">
          <Sparkles size={13} className="text-[#c9954a]" />
          <span>智能刮削 (TMDB)</span>
        </button>
        <button onClick={onManualScrape} className="btn-outline h-9 rounded-lg px-3 text-xs gap-1.5 border-gray-200 hover:border-brand-500/50 hover:bg-brand-50">
          <Search size={13} className="text-[#c9954a]" />
          <span>手动匹配刮削</span>
        </button>
        <button onClick={onMetadataEdit} className="btn-outline h-9 rounded-lg px-3 text-xs gap-1.5 border-gray-200 hover:border-brand-500/50 hover:bg-brand-50">
          <Pencil size={13} className="text-brand-500" />
          <span>编辑元数据</span>
        </button>
        <button onClick={onOrganize} className="btn-outline h-9 rounded-lg px-3 text-xs gap-1.5 border-gray-200 hover:border-brand-500/50 hover:bg-brand-50">
          <FolderInput size={13} className="text-[#c9954a]" />
          <span>整理入库</span>
        </button>
        <button onClick={onProbe} className="btn-outline h-9 rounded-lg px-3 text-xs gap-1.5 border-gray-200 hover:border-brand-500/50 hover:bg-brand-50">
          <Database size={13} className="text-brand-500" />
          <span>探测媒体轨 (ffprobe)</span>
        </button>
        <button onClick={onExportNFO} className="btn-outline h-9 rounded-lg px-3 text-xs gap-1.5 border-gray-200 hover:border-brand-500/50 hover:bg-brand-50">
          <FileText size={13} className="text-brand-500" />
          <span>写出本地 NFO 属性</span>
        </button>
        <button
          onClick={onDelete}
          className="btn-outline ml-auto h-9 rounded-lg px-3 text-xs gap-1.5 !border-red-100 !text-red-500 hover:!bg-red-50 hover:!border-red-200"
        >
          <Trash2 size={13} className="text-red-500" />
          <span>删除</span>
        </button>
      </div>
    </section>
  )
}

function isEpisodeArtworkTarget(media: Media): boolean {
  return media.season_num > 0 || media.episode_num > 0
}
