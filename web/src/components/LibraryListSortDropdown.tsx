import { useEffect, useRef, useState } from 'react'
import { ArrowDown, ArrowUp, ArrowUpDown, Check } from 'lucide-react'

import {
  LIBRARY_LIST_SORT_OPTIONS,
  type LibraryListSortField,
  type LibraryListSortOption,
  type LibraryListSortOrder,
} from '../utils/libraryListSort'

type LibraryListSortDropdownProps = {
  value: LibraryListSortField
  order: LibraryListSortOrder
  onChange: (field: LibraryListSortField, order: LibraryListSortOrder) => void
  className?: string
}

export function LibraryListSortDropdown({
  value,
  order,
  onChange,
  className = '',
}: LibraryListSortDropdownProps) {
  const [isOpen, setIsOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const currentOption = LIBRARY_LIST_SORT_OPTIONS.find((opt) => opt.id === value) ?? LIBRARY_LIST_SORT_OPTIONS[0]

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }
    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside)
    }
    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [isOpen])

  const handleSelect = (option: LibraryListSortOption) => {
    if (value === option.id) {
      onChange(option.id, order === 'asc' ? 'desc' : 'asc')
    } else {
      onChange(option.id, option.defaultOrder)
    }
    setIsOpen(false)
  }

  return (
    <div className={`relative inline-block text-left ${className}`} ref={dropdownRef}>
      <button
        type="button"
        onClick={() => setIsOpen((prev) => !prev)}
        className="inline-flex h-9 items-center gap-1.5 rounded-xl border border-sand-200 bg-white/90 px-3 py-1.5 text-xs font-semibold text-ink-600 shadow-sm transition-all hover:border-brand-300 hover:bg-brand-50/50 hover:text-brand-700 sm:h-10 sm:text-sm"
        title="更改媒体库列表排序"
      >
        <ArrowUpDown size={14} className="text-sand-500" />
        <span>排序: {currentOption.label}</span>
        {order === 'asc' ? (
          <ArrowUp size={13} className="font-bold text-brand-600" />
        ) : (
          <ArrowDown size={13} className="font-bold text-brand-600" />
        )}
      </button>

      {isOpen && (
        <div className="absolute left-0 z-50 mt-1.5 w-44 max-w-[calc(100vw-2rem)] origin-top-left rounded-2xl border border-sand-200/80 bg-white/95 p-1.5 shadow-xl backdrop-blur-md transition-all animate-in fade-in-0 zoom-in-95 sm:left-auto sm:right-0 sm:origin-top-right">
          <div className="border-b border-sand-100 px-2.5 py-1.5 text-[11px] font-bold text-sand-500">
            排序方式
          </div>
          <div className="space-y-0.5 py-1">
            {LIBRARY_LIST_SORT_OPTIONS.map((option) => {
              const isSelected = value === option.id
              return (
                <button
                  key={option.id}
                  type="button"
                  onClick={() => handleSelect(option)}
                  className={`flex w-full items-center justify-between rounded-xl px-3 py-2 text-xs font-medium transition-colors ${
                    isSelected
                      ? 'bg-brand-50 font-semibold text-brand-700'
                      : 'text-ink-600 hover:bg-sand-50 hover:text-brand-600'
                  }`}
                >
                  <div className="flex items-center gap-2">
                    {isSelected && <Check size={13} className="text-brand-600" />}
                    <span className={isSelected ? '' : 'pl-5'}>{option.label}</span>
                  </div>
                  {isSelected && (
                    <div className="flex items-center text-brand-600">
                      {order === 'asc' ? (
                        <ArrowUp size={14} className="stroke-[2.5]" />
                      ) : (
                        <ArrowDown size={14} className="stroke-[2.5]" />
                      )}
                    </div>
                  )}
                </button>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
