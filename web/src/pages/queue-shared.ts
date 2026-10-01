import { useCallback, useState } from 'react'
import toast from 'react-hot-toast'

/** 复制文本到剪贴板并提示（Strm/Scraper 队列页共用）。 */
export function copyToClipboard(text: string, label: string) {
  navigator.clipboard.writeText(text)
  toast.success(`已复制${label}`)
}

/**
 * 任务列表多选状态：StrmQueuePage 与 ScraperQueuePage 共用。
 * allIds 变化（翻页/筛选）时调用 reset() 清空选中。
 */
export function useTaskSelection() {
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set())

  const reset = useCallback(() => setSelectedIds(new Set()), [])

  const toggleRow = useCallback((id: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  const toggleAll = useCallback((visibleIds: string[]) => {
    setSelectedIds((prev) => {
      if (visibleIds.length > 0 && visibleIds.every((id) => prev.has(id))) {
        return new Set()
      }
      return new Set(visibleIds)
    })
  }, [])

  return { selectedIds, setSelectedIds, reset, toggleRow, toggleAll }
}
