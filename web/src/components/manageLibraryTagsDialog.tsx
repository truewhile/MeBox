import { createRoot } from 'react-dom/client'

import {
  ManageLibraryTagsDialogView,
  type ManageLibraryTagsDialogProps,
} from './ManageLibraryTagsDialogView'

export type OpenManageLibraryTagsDialogOptions = Omit<ManageLibraryTagsDialogProps, 'onClose'>

/** 以独立根节点打开标签管理对话框，关闭后 resolve。 */
export function openManageLibraryTagsDialog(
  options: OpenManageLibraryTagsDialogOptions,
): Promise<void> {
  return new Promise((resolve) => {
    const host = document.createElement('div')
    document.body.appendChild(host)
    const root = createRoot(host)

    const close = () => {
      root.unmount()
      host.remove()
      resolve()
    }

    root.render(<ManageLibraryTagsDialogView {...options} onClose={close} />)
  })
}
