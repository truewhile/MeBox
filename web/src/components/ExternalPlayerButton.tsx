import { useState } from 'react'
import toast from 'react-hot-toast'
import { Copy, ExternalLink, PlaySquare, X } from 'lucide-react'

import { playbackAPI, type ExternalPlayer } from '../api/playback'

export function ExternalPlayerButton({
  mediaId,
  label = '外部播放器',
  compact = false,
}: {
  mediaId: string
  label?: string
  compact?: boolean
}) {
  const [open, setOpen] = useState(false)
  const [loading, setLoading] = useState(false)
  const [players, setPlayers] = useState<ExternalPlayer[]>([])
  const [streamURL, setStreamURL] = useState('')

  const load = async () => {
    setLoading(true)
    try {
      const [playerData, urlData] = await Promise.all([
        playbackAPI.externalPlayers(mediaId),
        playbackAPI.externalURL(mediaId),
      ])
      setPlayers(playerData.players ?? [])
      setStreamURL(urlData.url)
      setOpen(true)
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '生成外部播放链接失败'
      toast.error(msg)
    } finally {
      setLoading(false)
    }
  }

  return (
    <>
      <button
        type="button"
        title={compact ? '使用外部播放器播放' : undefined}
        disabled={loading}
        onClick={(event) => {
          event.preventDefault()
          event.stopPropagation()
          load()
        }}
        className={
          compact
            ? 'shrink-0 inline-flex items-center rounded-lg border border-primary-400/35 bg-white px-2 py-1 text-xs font-semibold text-brand-500 hover:bg-primary-400/10 disabled:opacity-50 transition-colors whitespace-nowrap'
            : 'btn-outline h-11 border-brand-500/30 px-5 text-[#c9954a] hover:border-brand-500 hover:bg-brand-50'
        }
      >
        <PlaySquare size={compact ? 13 : 14} className="mr-1 inline" />
        {loading ? '生成中…' : label}
      </button>
      {open && (
        <ExternalPlayerModal
          players={players}
          streamURL={streamURL}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

function ExternalPlayerModal({
  players,
  streamURL,
  onClose,
}: {
  players: ExternalPlayer[]
  streamURL: string
  onClose: () => void
}) {
  const copy = async (value: string) => {
    await navigator.clipboard.writeText(value)
    toast.success('播放链接已复制')
  }

  return (
    <div className="fixed inset-0 z-[90] flex items-center justify-center bg-black/40 p-4 backdrop-blur-sm" onClick={onClose}>
      <div className="w-full max-w-2xl overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl" onClick={(event) => event.stopPropagation()}>
        <div className="flex items-start justify-between gap-4 border-b border-gray-100 p-5">
          <div>
            <h3 className="font-display text-xl font-bold text-ink-600">外部播放器播放</h3>
            <p className="mt-1 text-xs text-ink-50">链接已包含临时播放 Token，可复制到 VLC、Infuse、VidHub、SenPlayer 等客户端。</p>
          </div>
          <button onClick={onClose} className="rounded-xl p-2 text-ink-50 hover:bg-gray-100 hover:text-ink-600">
            <X size={18} />
          </button>
        </div>
        <div className="space-y-4 p-5">
          <div className="rounded-2xl border border-gray-100 bg-gray-50 p-3">
            <div className="mb-2 text-xs font-semibold text-sand-500">直链播放地址</div>
            <div className="flex gap-2">
              <input readOnly value={streamURL} className="input-base min-w-0 flex-1 font-mono text-xs" />
              <button onClick={() => copy(streamURL)} className="btn-outline shrink-0 px-3">
                <Copy size={14} />
                复制
              </button>
            </div>
          </div>
          <div className="grid gap-2 sm:grid-cols-2">
            {players.map((player) => (
              <a
                key={player.name}
                href={player.url}
                className="flex items-center justify-between rounded-2xl border border-gray-100 bg-white px-4 py-3 text-sm font-semibold text-ink-600 shadow-sm hover:border-brand-300 hover:bg-brand-50"
              >
                <span>{player.name}</span>
                <ExternalLink size={15} className="text-brand-500" />
              </a>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
