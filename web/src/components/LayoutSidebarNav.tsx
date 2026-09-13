import type { ReactNode } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { ChevronDown } from 'lucide-react'
import clsx from 'clsx'

import { isSidebarLinkActive } from './layoutNavigation'

type SidebarGroupProps = {
  id: string
  icon: ReactNode
  label: string
  children: ReactNode
  collapsed?: boolean
  open?: boolean
  active?: boolean
  onToggle: (id: string) => void
}

export function SidebarGroup({ id, icon, label, children, collapsed, open, active, onToggle }: SidebarGroupProps) {
  return (
    <div className="space-y-1">
      <button
        type="button"
        onClick={() => onToggle(id)}
        className={clsx(
          'group relative flex w-full items-center gap-3.5 rounded-xl px-4 py-3 text-sm font-bold transition-all duration-300',
          active
            ? 'bg-[var(--app-active-bg)] text-[var(--app-active-text)] shadow-sm'
            : 'text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]',
          collapsed && 'justify-center px-0',
        )}
      >
        <span className={clsx(
          'flex h-5 w-5 shrink-0 items-center justify-center',
          active ? 'text-[var(--app-active-icon)]' : 'text-[var(--app-muted)] group-hover:text-[var(--app-subtle)]',
        )}>
          {icon}
        </span>
        {!collapsed && (
          <>
            <span className="flex-1 truncate text-left">{label}</span>
            <ChevronDown
              size={14}
              className={clsx('transition-transform duration-200', open && 'rotate-180')}
            />
          </>
        )}
        {collapsed && (
          <div className="absolute left-full z-50 ml-3 rounded-xl bg-[var(--app-tooltip-bg)] px-2.5 py-1.5 text-xs font-semibold text-[var(--app-tooltip-text)] opacity-0 shadow-lg transition-opacity group-hover:opacity-100">
            {label}
          </div>
        )}
      </button>
      {!collapsed && open && (
        <div className="overflow-hidden animate-accordion-in">
          <div className="space-y-1 pb-1 pl-3">
            {children}
          </div>
        </div>
      )}
    </div>
  )
}

type SidebarLinkProps = {
  to: string
  icon: ReactNode
  label: string
  end?: boolean
  collapsed?: boolean
  child?: boolean
}

export function SidebarLink({ to, icon, label, end, collapsed, child }: SidebarLinkProps) {
  const location = useLocation()
  const isActive = isSidebarLinkActive(to, location.pathname, location.search, end)

  return (
    <Link
      to={to}
      aria-current={isActive ? 'page' : undefined}
      className={clsx(
        'relative flex items-center gap-3.5 rounded-xl px-4 py-3 text-sm font-semibold transition-all duration-300 group',
        child && 'py-2.5 text-[13px]',
        isActive
          ? 'bg-[var(--app-active-bg)] text-[var(--app-active-text)] shadow-sm'
          : 'text-[var(--app-muted)] hover:bg-[var(--app-hover)] hover:text-[var(--app-text)]',
      )}
    >
      <span className={clsx(
        'flex h-5 w-5 shrink-0 items-center justify-center transition-transform duration-300 group-hover:scale-110',
        isActive ? 'text-[var(--app-active-icon)]' : 'text-[var(--app-muted)] group-hover:text-[var(--app-subtle)]',
      )}>
        {icon}
      </span>
      {!collapsed && (
        <span className="truncate whitespace-nowrap">
          {label}
        </span>
      )}
      {collapsed && (
        <div className="pointer-events-none absolute left-full z-50 ml-3 whitespace-nowrap rounded-xl bg-[var(--app-tooltip-bg)] px-2.5 py-1.5 text-xs font-semibold text-[var(--app-tooltip-text)] opacity-0 shadow-lg transition-opacity group-hover:pointer-events-auto group-hover:opacity-100">
          {label}
        </div>
      )}
    </Link>
  )
}
