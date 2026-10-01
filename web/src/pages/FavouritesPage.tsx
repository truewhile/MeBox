import { useEffect, useState } from 'react'
import toast from 'react-hot-toast'

import { playbackAPI } from '../api/playback'
import { MediaCard } from '../components/MediaCard'
import type { Media } from '../types'

export function FavouritesPage() {
  const [items, setItems] = useState<Media[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  useEffect(() => {
    playbackAPI
      .listFavourites()
      .then((data) => setItems(data ?? []))
      .catch((err) => {
        const msg =
          (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
          '加载收藏失败'
        setError(msg)
        toast.error(msg)
      })
      .finally(() => setLoading(false))
  }, [])

  const isEmpty = !loading && !error && items.length === 0

  return (
    <div className="space-y-6">
      <h1 className="font-display text-3xl font-bold text-ink-600">我的收藏</h1>

      {loading && (
        <div className="flex items-center gap-2 py-8 text-ink-50">
          <span className="inline-block h-4 w-4 animate-spin rounded-full border-2 border-primary-400 border-t-transparent" />
          加载中…
        </div>
      )}

      {error && (
        <div className="glass-panel !border-red-400/30 p-4 text-sm text-red-400">
          {error}
          <button
            className="ml-3 underline hover:text-red-300"
            onClick={() => {
              setError('')
              setLoading(true)
              playbackAPI
                .listFavourites()
                .then((data) => setItems(data ?? []))
                .catch((err2) => {
                  const msg =
                    (err2 as { response?: { data?: { error?: string } } })?.response?.data?.error ??
                    '加载收藏失败'
                  setError(msg)
                })
                .finally(() => setLoading(false))
            }}
          >
            重试
          </button>
        </div>
      )}

      {isEmpty && (
        <div className="glass-panel flex flex-col items-center gap-3 p-10 text-center">
          <p className="text-lg text-ink-100">还没有任何收藏</p>
          <p className="text-sm text-sand-500">
            在媒体库或详情页点击「加入收藏」即可添加喜欢的内容
          </p>
        </div>
      )}

      {items.length > 0 && (
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6">
          {items.map((m) => (
            <MediaCard key={m.id} media={m} linkState={{ from: '/favourites' }} />
          ))}
        </div>
      )}
    </div>
  )
}
