import { useCallback, useEffect, useRef, useState } from 'react'

import {
  loadPinnedLibraryIds,
  savePinnedLibraryIds,
  togglePinnedLibraryId,
} from '../utils/pinnedLibraries'

export function usePinnedLibraries() {
  const [pinnedIds, setPinnedIds] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [syncing, setSyncing] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const loadedRef = useRef(false)
  const pinnedIdsRef = useRef<string[]>([])
  const syncingRef = useRef(false)

  useEffect(() => {
    pinnedIdsRef.current = pinnedIds
  }, [pinnedIds])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setLoadError(false)
    loadedRef.current = false
    loadPinnedLibraryIds()
      .then((ids) => {
        if (cancelled) return
        loadedRef.current = true
        pinnedIdsRef.current = ids
        setPinnedIds(ids)
        setLoadError(false)
      })
      .catch(() => {
        if (cancelled) return
        loadedRef.current = false
        setLoadError(true)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [])

  const togglePin = useCallback(async (libraryId: string) => {
    if (!loadedRef.current || loading || loadError || syncingRef.current) return

    // Compute the next list synchronously from a ref. Do NOT capture the next
    // value inside setState updater callbacks — React may defer those, leaving
    // optimistic as [] and wiping the server-side pin list.
    const previous = pinnedIdsRef.current
    const optimistic = togglePinnedLibraryId(previous, libraryId)

    pinnedIdsRef.current = optimistic
    setPinnedIds(optimistic)
    syncingRef.current = true
    setSyncing(true)
    try {
      const saved = await savePinnedLibraryIds(optimistic)
      pinnedIdsRef.current = saved
      setPinnedIds(saved)
    } catch {
      pinnedIdsRef.current = previous
      setPinnedIds(previous)
    } finally {
      syncingRef.current = false
      setSyncing(false)
    }
  }, [loading, loadError])

  return { pinnedIds, loading, syncing, loadError, togglePin }
}
