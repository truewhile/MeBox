import { useCallback, useEffect, useState } from 'react'
import toast from 'react-hot-toast'

import { playbackAPI } from '../api/playback'

export function useFavourites() {
  const [favouriteIDs, setFavouriteIDs] = useState<Set<string>>(() => new Set())
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    playbackAPI
      .listFavouriteIDs()
      .then((items) => {
        if (!cancelled) {
          setFavouriteIDs(new Set(items ?? []))
        }
      })
      .catch(() => {
        if (!cancelled) toast.error('加载收藏状态失败')
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const isFavourite = useCallback((mediaID: string) => favouriteIDs.has(mediaID), [favouriteIDs])

  const toggleFavourite = useCallback(async (mediaID: string) => {
    try {
      const state = await playbackAPI.toggleFavourite(mediaID)
      setFavouriteIDs((current) => {
        const next = new Set(current)
        if (state) next.add(mediaID)
        else next.delete(mediaID)
        return next
      })
      toast.success(state ? '已加入我的收藏' : '已取消收藏')
      return state
    } catch {
      toast.error('收藏操作失败')
      return favouriteIDs.has(mediaID)
    }
  }, [favouriteIDs])

  return { isFavourite, toggleFavourite, loading }
}
