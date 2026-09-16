import { useCallback, useState, type DragEvent } from 'react'

/** 一组可直接展开到可拖拽元素上的事件处理器。 */
export type DragReorderHandlers = {
  draggable: boolean
  onDragStart: (event: DragEvent) => void
  onDragOver: (event: DragEvent) => void
  onDragLeave: (event: DragEvent) => void
  onDrop: (event: DragEvent) => void
  onDragEnd: () => void
}

/**
 * 把 `fromId` 移动到 `toId` 当前所在的位置，返回新数组；不改动入参。
 * 任一 id 不存在，或两者相同时返回 null，表示无需重排。
 */
export function moveIdToPosition(ids: string[], fromId: string, toId: string): string[] | null {
  const fromIndex = ids.indexOf(fromId)
  const toIndex = ids.indexOf(toId)
  if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) return null
  const next = [...ids]
  const [moved] = next.splice(fromIndex, 1)
  next.splice(toIndex, 0, moved)
  return next
}

/**
 * 列表拖拽排序的通用状态机：只负责「一串稳定 id」的拖拽与重排计算，
 * 不关心渲染。调用方把 `dragProps(id)` 展开到拖拽柄上，并用返回的
 * `draggingId` / `dragOverId` 决定高亮样式。
 *
 * 落点语义：把被拖项插入到目标项所在的位置。未传 `onReorder` 时整体禁用。
 */
export function useDragReorder(
  ids: string[],
  onReorder?: (orderedIds: string[]) => void | Promise<void>,
): {
  draggingId: string | null
  dragOverId: string | null
  dragProps: (id: string) => DragReorderHandlers
} {
  const [draggingId, setDraggingId] = useState<string | null>(null)
  const [dragOverId, setDragOverId] = useState<string | null>(null)

  const enabled = Boolean(onReorder)

  const dragProps = useCallback(
    (id: string): DragReorderHandlers => ({
      draggable: enabled,
      onDragStart: (event) => {
        if (!enabled) return
        event.dataTransfer.effectAllowed = 'move'
        event.dataTransfer.setData('text/plain', id)
        setDraggingId(id)
        setDragOverId(null)
      },
      onDragOver: (event) => {
        if (!enabled || draggingId === id) return
        event.preventDefault()
        event.dataTransfer.dropEffect = 'move'
        setDragOverId((prev) => (prev === id ? prev : id))
      },
      onDragLeave: () => {
        setDragOverId((prev) => (prev === id ? null : prev))
      },
      onDrop: (event) => {
        if (!enabled) return
        event.preventDefault()
        // 拖拽期间 state 可能尚未提交，优先信任 dataTransfer 里的来源 id。
        const fromId = draggingId ?? event.dataTransfer.getData('text/plain')
        setDraggingId(null)
        setDragOverId(null)
        if (!fromId || fromId === id) return
        const next = moveIdToPosition(ids, fromId, id)
        if (next) void onReorder?.(next)
      },
      onDragEnd: () => {
        setDraggingId(null)
        setDragOverId(null)
      },
    }),
    [enabled, draggingId, ids, onReorder],
  )

  return { draggingId, dragOverId, dragProps }
}