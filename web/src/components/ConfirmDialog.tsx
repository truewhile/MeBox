import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'

export type ConfirmOptions = {
  title?: string
  message: string
  confirmText?: string
  cancelText?: string
  danger?: boolean
  checkboxLabel?: string
  checkboxDefaultChecked?: boolean
}

export type ConfirmResult = {
  confirmed: boolean
  checked: boolean
}

export function ConfirmDialog({
  options,
  onClose,
}: {
  options: ConfirmOptions
  onClose: (value: ConfirmResult) => void
}) {
  const danger = options.danger ?? true
  const hasCheckbox = Boolean(options.checkboxLabel)
  const [checked, setChecked] = useState(Boolean(options.checkboxDefaultChecked))
  return (
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black/35 p-4 backdrop-blur-sm"
      onClick={() => onClose({ confirmed: false, checked })}
    >
      <div
        role="dialog"
        aria-modal="true"
        className="w-full max-w-md overflow-hidden rounded-3xl border border-white/70 bg-white shadow-2xl"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="flex gap-4 p-5">
          <div
            className={
              'flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl ' +
              (danger ? 'bg-red-50 text-red-500' : 'bg-primary-400/10 text-brand-500')
            }
          >
            <AlertTriangle size={22} />
          </div>
          <div className="min-w-0 flex-1">
            <h3 className="font-display text-lg font-bold text-ink-600">
              {options.title || '确认操作'}
            </h3>
            <p className="mt-2 whitespace-pre-line text-sm leading-6 text-ink-50">{options.message}</p>
            {hasCheckbox ? (
              <label className="mt-4 flex cursor-pointer items-start gap-2.5 text-sm text-ink-100">
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 rounded border-gray-300 text-red-500 focus:ring-red-400"
                  checked={checked}
                  onChange={(event) => setChecked(event.target.checked)}
                />
                <span>{options.checkboxLabel}</span>
              </label>
            ) : null}
          </div>
        </div>
        <div className="flex justify-end gap-2 border-t border-gray-100 bg-gray-50/80 px-5 py-4">
          <button
            type="button"
            onClick={() => onClose({ confirmed: false, checked })}
            className="rounded-xl border border-gray-200 bg-white px-4 py-2 text-sm font-semibold text-ink-100 hover:bg-gray-50"
          >
            {options.cancelText || '取消'}
          </button>
          <button
            type="button"
            onClick={() => onClose({ confirmed: true, checked })}
            className={
              'rounded-xl px-4 py-2 text-sm font-semibold text-white shadow-sm transition ' +
              (danger ? 'bg-red-500 hover:bg-red-600' : 'bg-brand-500 hover:bg-brand-600')
            }
          >
            {options.confirmText || '确认'}
          </button>
        </div>
      </div>
    </div>
  )
}
