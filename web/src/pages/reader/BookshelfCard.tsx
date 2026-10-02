import { BookOpen, Clock, HardDrive, History, UserRound } from 'lucide-react'

import type { ReaderBook } from '../../api/reader'
import { BookCardMenu } from '../../components/BookCardMenu'
import ReaderBookCover from '../../components/ReaderBookCover'
import type { ReaderShelfLayout } from '../../stores/readerSettings'
import { formatRelativeTime, readProgressText, shelfUpdateTime, unreadChapters } from './bookshelfModel'

// 书架卡片：网格 / 列表 / 紧凑列表三种布局共用。
//
// 信息层级对齐 legado 书架物品：
//   网格   —— 封面 + 本地角标 + 未读徽标 + 书名（两行）
//   列表   —— 封面 + 书名 + 作者 + 读到 + 最新章节 + 更新时间 + 未读徽标
//   紧凑   —— 小封面 + 书名 + 作者 · 读到
// 点卡片进阅读器；「详情」进书籍详情页；「移出」二次确认由调用方处理。

function UnreadBadge({ book, compact = false }: { book: ReaderBook; compact?: boolean }) {
  const unread = unreadChapters(book)
  if (unread === null) return null
  return (
    <span
      title={unread > 0 ? `还有 ${unread} 章未读` : '已读完'}
      className={`shrink-0 whitespace-nowrap rounded-md font-bold ${
        compact ? 'px-1 py-px text-[10px]' : 'px-1.5 py-0.5 text-2xs'
      } ${unread > 0 ? 'bg-rose-500/90 text-white' : 'bg-[var(--app-hover)] text-[var(--app-muted)]'}`}
    >
      {unread > 0 ? `${unread} 章未读` : '已读完'}
    </span>
  )
}

function LocalBadge() {
  return (
    <span className="flex items-center gap-1 whitespace-nowrap rounded-lg bg-black/55 px-1.5 py-0.5 text-[10px] font-bold text-white backdrop-blur">
      <HardDrive size={10} /> 本地
    </span>
  )
}

function GridCard({
  book,
  showUnread,
  onOpen,
  onPickGroup,
  onRemove,
}: {
  book: ReaderBook
  showUnread: boolean
  onOpen: () => void
  onPickGroup: () => void
  onRemove: () => void
}) {
  return (
    <div className="group">
      <div
        role="button"
        tabIndex={0}
        data-book-card=""
        onClick={onOpen}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onOpen()
          }
        }}
        className="relative w-full cursor-pointer overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] shadow-sm transition group-hover:shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500"
      >
        <div className="aspect-[3/4] w-full">
          <ReaderBookCover url={book.cover_url} alt={book.name} iconSize={22} />
        </div>
        {/* 左下角：本地来源标记（右上角留给未读徽标，右下角是详情/移出按钮） */}
        {book.is_local && (
          <span className="absolute bottom-1 left-1">
            <LocalBadge />
          </span>
        )}
        {showUnread && unreadChapters(book) !== null && (
          <span className="absolute right-1 top-1">
            <UnreadBadge book={book} />
          </span>
        )}
        <div className="absolute bottom-1 right-1">
          <BookCardMenu book={book} variant="overlay" onPickGroup={onPickGroup} onRemove={onRemove} />
        </div>
      </div>
      <p className="mt-2 line-clamp-2 text-xs font-bold leading-snug text-[var(--app-text)]">{book.name}</p>
    </div>
  )
}

function ListCard({
  book,
  showUnread,
  showUpdateTime,
  groupName = '',
  compact = false,
  onOpen,
  onPickGroup,
  onRemove,
}: {
  book: ReaderBook
  showUnread: boolean
  showUpdateTime: boolean
  /** 所属分组名；空串表示未分组。 */
  groupName?: string
  compact?: boolean
  onOpen: () => void
  onPickGroup: () => void
  onRemove: () => void
}) {
  const read = readProgressText(book)
  const updated = shelfUpdateTime(book)
  const coverClass = compact ? 'h-16 w-12' : 'h-[90px] w-[66px]'

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onOpen()
        }
      }}
      className={`group relative flex cursor-pointer gap-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-panel-soft)] p-2.5 shadow-sm transition hover:border-brand-500/40 hover:shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500 ${
        compact ? 'items-center' : 'items-stretch'
      }`}
    >
      <div className={`relative shrink-0 overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-panel)] ${coverClass}`}>
        <ReaderBookCover url={book.cover_url} alt={book.name} iconSize={compact ? 16 : 20} />
        {book.is_local && (
          <span className="absolute left-0.5 top-0.5 rounded-md bg-black/55 p-0.5 text-white backdrop-blur">
            <HardDrive size={9} />
          </span>
        )}
      </div>

      <div className="flex min-w-0 flex-1 flex-col justify-center gap-1">
        <div className="flex items-center gap-2">
          <p className="min-w-0 flex-1 truncate text-sm font-bold text-[var(--app-text)]">{book.name}</p>
          {showUnread && <UnreadBadge book={book} compact={compact} />}
        </div>

        {compact ? (
          <p className="flex min-w-0 items-center gap-1 truncate text-2xs text-[var(--app-muted)]">
            <span className="truncate">{book.author || '佚名'}</span>
            {read && (
              <>
                <span className="shrink-0">·</span>
                <span className="truncate">读到 {read}</span>
              </>
            )}
          </p>
        ) : (
          <>
            <p className="flex min-w-0 items-center gap-1 text-2xs text-[var(--app-muted)]">
              <UserRound size={11} className="shrink-0 opacity-60" />
              <span className="truncate">{book.author || '佚名'}</span>
              {groupName && (
                <>
                  <span className="shrink-0">·</span>
                  <span className="shrink-0 rounded-md bg-brand-500/10 px-1.5 py-px text-[10px] font-bold text-brand-600">
                    {groupName}
                  </span>
                </>
              )}
              {showUpdateTime && updated > 0 && (
                <>
                  <span className="shrink-0">·</span>
                  <Clock size={11} className="shrink-0 opacity-60" />
                  <span className="shrink-0">{formatRelativeTime(updated)}</span>
                </>
              )}
            </p>
            <p className="flex min-w-0 items-center gap-1 text-2xs text-[var(--app-muted)]">
              <History size={11} className="shrink-0 opacity-60" />
              <span className="truncate">{read ? `读到 ${read}` : '尚未开始阅读'}</span>
            </p>
            <p className="flex min-w-0 items-center gap-1 text-2xs text-[var(--app-muted)]">
              <BookOpen size={11} className="shrink-0 opacity-60" />
              <span className="truncate">
                最新 {book.latest_chapter_title || '未知'}
              </span>
            </p>
          </>
        )}
      </div>

      <div className="flex shrink-0 items-center justify-center">
        <BookCardMenu book={book} variant="plain" onPickGroup={onPickGroup} onRemove={onRemove} />
      </div>
    </div>
  )
}

export function BookshelfCard({
  book,
  layout,
  showUnread,
  showUpdateTime,
  groupName = '',
  onOpen,
  onPickGroup,
  onRemove,
}: {
  book: ReaderBook
  layout: ReaderShelfLayout
  showUnread: boolean
  showUpdateTime: boolean
  /** 所属分组名；空串表示未分组（只在列表布局里展示）。 */
  groupName?: string
  onOpen: () => void
  onPickGroup: () => void
  onRemove: () => void
}) {
  if (layout === 'grid') {
    return (
      <GridCard book={book} showUnread={showUnread} onOpen={onOpen} onPickGroup={onPickGroup} onRemove={onRemove} />
    )
  }
  return (
    <ListCard
      book={book}
      compact={layout === 'compact'}
      showUnread={showUnread}
      showUpdateTime={showUpdateTime}
      groupName={groupName}
      onOpen={onOpen}
      onPickGroup={onPickGroup}
      onRemove={onRemove}
    />
  )
}
