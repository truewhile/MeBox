import { createRoot } from 'react-dom/client'

import { ManageLibrariesDialogView } from './ManageLibrariesDialogView'

export function openManageLibrariesDialog(): Promise<void> {
  return new Promise((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)

    const close = () => {
      root.unmount()
      host.remove()
      resolve()
    }

    root.render(<ManageLibrariesDialogView onClose={close} />)
  })
}