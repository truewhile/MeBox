import { useCallback, useEffect, useState } from 'react'

import {
  readLibraryListSort,
  writeLibraryListSort,
  type LibraryListSortField,
  type LibraryListSortOrder,
} from '../utils/libraryListSort'

export type UseLibraryListSortResult = {
  field: LibraryListSortField
  order: LibraryListSortOrder
  setSort: (field: LibraryListSortField, order: LibraryListSortOrder) => void
}

/**
 * 媒体库列表的排序偏好（localStorage 持久化）。
 *
 * 首页和 /libraries 读的是同一份偏好，这样在 /libraries 改了排序后回到首页
 * 顺序也一致；默认「库名倒序」。另外监听 storage 事件，多标签页同时打开时
 * 一边改动另一边也能跟上。
 */
export function useLibraryListSort(): UseLibraryListSortResult {
  const [sort, setSort] = useState(() => readLibraryListSort())

  useEffect(() => {
    const sync = () => setSort(readLibraryListSort())
    window.addEventListener('storage', sync)
    return () => window.removeEventListener('storage', sync)
  }, [])

  const update = useCallback((field: LibraryListSortField, order: LibraryListSortOrder) => {
    writeLibraryListSort(field, order)
    setSort({ field, order })
  }, [])

  return { field: sort.field, order: sort.order, setSort: update }
}
