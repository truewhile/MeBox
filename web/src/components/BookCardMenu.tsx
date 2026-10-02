import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { FolderInput, Info, MoreVertical, Trash2 } from 'lucide-react'

import type { ReaderBook } from '../api/reader'
import { bookDetailPath } from '../pages/reader/bookshelfModel'

/**
 * 书籍卡片上的「⋯」操作菜单：把「移到分组 / 书籍详情 / 移出书架」收进一个下拉里，
 * 不再让三个图标直接铺在封面上。
 *
 * 定位上有三点刻意的处理：
 *  1. 菜单用 `position: fixed` + 实测位置渲染（而不是绝对定位挂在卡片里）。
 *     网格卡片为了裁圆角带 `overflow-hidden`，绝对定位的子元素会被裁掉；固定定位能
 *     逃出这个裁剪，也不用引入 portal。
 *  2. 网格布局下菜单与书籍卡片**同宽、左边缘对齐**、向下展开，看起来像卡片自己展开的
 *     动作面板；宽度取自卡片（`data-book-card` 标记的元素），列数变化时自动跟随。
 *  3. 下方确实放不下时才向上翻，并夹在视口内，避免贴底部的卡片点开看不见。
 */
export function BookCardMenu({
  book,
  onPickGroup,
  onRemove,
  variant = 'overlay',
}: {
  book: ReaderBook
  onPickGroup: () => void
  onRemove: () => void
  /** overlay：网格封面上的圆形按钮；plain：列表布局右侧的浅色按钮。 */
  variant?: 'overlay' | 'plain'
}) {
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  // 进场动画用：先以「缩起来」渲染一帧，再切到展开态触发过渡
  const [shown, setShown] = useState(false)
  const [pos, setPos] = useState<{ top: number; left: number; width: number; up: boolean } | null>(null)
  const btnRef = useRef<HTMLButtonElement>(null)

  /** 列表布局（一行很宽）不适合跟行同宽，保持固定宽度。 */
  const PLAIN_WIDTH = 176
  /** 网格卡片过窄时给个下限，保证文字不挤成两行。 */
  const MIN_WIDTH = 132
  /** 三个菜单项 + 分隔线的大致高度，用来判断下方是否放得下。 */
  const MENU_HEIGHT = 140
  const GAP = 8
  const EDGE = 8

  const openMenu = () => {
    const el = btnRef.current
    if (!el) return
    const btnRect = el.getBoundingClientRect()

    // 网格：取卡片本身的位置与宽度（由 BookshelfCard 打上 data-book-card 标记）
    const cardEl = variant === 'overlay' ? el.closest('[data-book-card]') : null
    const cardRect = cardEl instanceof HTMLElement ? cardEl.getBoundingClientRect() : null

    const width = cardRect
      ? Math.max(MIN_WIDTH, Math.min(cardRect.width, window.innerWidth - EDGE * 2))
      : PLAIN_WIDTH
    const maxLeft = Math.max(EDGE, window.innerWidth - width - EDGE)
    const left = cardRect
      ? Math.min(Math.max(EDGE, cardRect.left), maxLeft)
      : Math.min(Math.max(EDGE, btnRect.right - width), maxLeft)

    // 默认向下；只有下方真的放不下才向上翻
    const spaceBelow = window.innerHeight - btnRect.bottom
    const up = spaceBelow < MENU_HEIGHT + GAP + EDGE && btnRect.top >= MENU_HEIGHT + GAP + EDGE
    const top = up
      ? Math.max(EDGE, btnRect.top - GAP - MENU_HEIGHT)
      : Math.min(btnRect.bottom + GAP, Math.max(EDGE, window.innerHeight - MENU_HEIGHT - EDGE))

    setShown(false)
    setPos({ top, left, width, up })
    setOpen(true)
  }

  useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (btnRef.current?.contains(target)) return
      // 菜单本身在 fixed 层里，点它不算「点外部」
      if (target instanceof Element && target.closest('[data-book-card-menu]')) return
      close()
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close()
    }
    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('keydown', onKeyDown)
    // 滚动/缩放后按钮位置会变，菜单会脱位，直接关掉更省事
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [open])

  // 挂载后下一帧再切到展开态，否则过渡不会触发
  useEffect(() => {
    if (!open) return
    const id = window.requestAnimationFrame(() => setShown(true))
    return () => window.cancelAnimationFrame(id)
  }, [open])

  const run = (action: () => void) => {
    setOpen(false)
    action()
  }

  const triggerClass =
    variant === 'overlay'
      ? 'rounded-full bg-black/55 p-1.5 text-white opacity-85 backdrop-blur transition hover:bg-brand-500 hover:opacity-100 focus-visible:opacity-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-white'
      : 'rounded-lg p-1.5 text-[var(--app-muted)] transition hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]'

  // 每行自己声明完整配色（含文字颜色），避免同类 utility 互相覆盖
  const itemBase =
    'flex w-full items-center gap-2 rounded-lg px-2.5 py-2.5 text-left text-xs font-bold transition-colors'
  const itemNormal = 'text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]'
  const itemDanger = 'text-red-500/90 hover:bg-red-500/10 hover:text-red-500'

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`更多操作：${book.name}`}
        title="更多操作"
        onClick={(event) => {
          event.stopPropagation()
          if (open) setOpen(false)
          else openMenu()
        }}
        className={triggerClass}
      >
        <MoreVertical size={variant === 'overlay' ? 13 : 16} />
      </button>

      {open && pos && (
        <div
          data-book-card-menu=""
          role="menu"
          style={{
            top: pos.top,
            left: pos.left,
            width: pos.width,
            transformOrigin: pos.up ? 'bottom right' : 'top right',
          }}
          className={`fixed z-[120] rounded-2xl border border-[var(--app-border)] bg-[var(--app-panel)] p-1.5 shadow-2xl transition duration-100 ease-out ${
            shown ? 'scale-100 opacity-100' : 'scale-95 opacity-0'
          }`}
          onClick={(event) => event.stopPropagation()}
        >
          <button type="button" role="menuitem" className={`${itemBase} ${itemNormal}`} onClick={() => run(onPickGroup)}>
            <FolderInput size={14} className="shrink-0 text-brand-500" /> 移到分组
          </button>
          <button
            type="button"
            role="menuitem"
            className={`${itemBase} ${itemNormal}`}
            onClick={() => run(() => navigate(bookDetailPath(book)))}
          >
            <Info size={14} className="shrink-0 opacity-70" /> 书籍详情
          </button>
          <div className="my-1 border-t border-[var(--app-border)]" />
          <button
            type="button"
            role="menuitem"
            className={`${itemBase} ${itemDanger}`}
            onClick={() => run(onRemove)}
          >
            <Trash2 size={14} className="shrink-0" /> 移出书架
          </button>
        </div>
      )}
    </>
  )
}
