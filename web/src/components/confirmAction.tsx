import { createRoot } from 'react-dom/client'

import { ConfirmDialog, type ConfirmOptions, type ConfirmResult } from './ConfirmDialog'

export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  return confirmActionResult(options).then((result) => result.confirmed)
}

export function confirmActionResult(options: ConfirmOptions): Promise<ConfirmResult> {
  return new Promise((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)
    const close = (value: ConfirmResult) => {
      root.unmount()
      host.remove()
      resolve(value)
    }
    root.render(<ConfirmDialog options={options} onClose={close} />)
  })
}
