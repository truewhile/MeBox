import { X } from 'lucide-react'

import { AdminLibraryPanel } from '../pages/AdminLibraryPanel'

export function ManageLibrariesDialogView({ onClose }: { onClose: () => void }) {
  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black/35 p-2 sm:p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="flex h-[calc(100dvh-1rem)] sm:h-[85vh] w-full max-w-5xl flex-col overflow-hidden rounded-2xl sm:rounded-3xl border border-white/70 bg-white shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-gray-100 px-4 py-3 sm:px-6 sm:py-4">
          <h3 className="font-display text-base sm:text-lg font-bold text-ink-600">管理媒体库</h3>
          <button
            type="button"
            onClick={onClose}
            className="rounded-xl p-1.5 text-ink-50 transition hover:bg-gray-100 hover:text-ink-600"
            title="关闭"
          >
            <X size={20} />
          </button>
        </div>
        <div className="flex-1 overflow-y-auto p-3 sm:p-6">
          <AdminLibraryPanel />
        </div>
      </div>
    </div>
  )
}
