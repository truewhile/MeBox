import { AdminLibraryCreateForm } from './AdminLibraryPanelSections'
import { AdminLibraryTable } from './AdminLibraryTable'
import { LibraryStorageStats } from './LibraryStorageStats'
import { useAdminLibraryPanel } from './useAdminLibraryPanel'

export function AdminLibraryPanel() {
  const { libs, createForm, editableRoots, rootActions, libraryActions } = useAdminLibraryPanel()

  return (
    <div className="space-y-8">
      <AdminLibraryCreateForm
        name={createForm.name}
        type={createForm.type}
        coverURL={createForm.coverURL}
        roots={createForm.roots}
        createPerSubfolder={createForm.createPerSubfolder}
        creating={createForm.creating}
        onNameChange={createForm.setName}
        onTypeChange={createForm.setType}
        onCoverURLChange={createForm.setCoverURL}
        onCreatePerSubfolderChange={createForm.setCreatePerSubfolder}
        onRootChange={createForm.updateRoot}
        onAddRoot={createForm.addRoot}
        onRemoveRoot={createForm.removeRoot}
        onSubmit={createForm.handleCreate}
      />
      <AdminLibraryTable
        libs={libs}
        editableRootDraft={editableRoots.editableRootDraft}
        onEditableRootChange={editableRoots.setEditableRootDraft}
        onSaveRoot={rootActions.saveLibraryRoot}
        onScanRoot={rootActions.scanLibraryRoot}
        onToggleRoot={rootActions.toggleLibraryRoot}
        onRemoveRoot={rootActions.removeLibraryRoot}
        onScanLibrary={libraryActions.scanLibrary}
        onRemoveLibrary={libraryActions.removeLibrary}
        onAddLibraryRoot={libraryActions.addLibraryRoot}
        onEditLibraryCover={libraryActions.editLibraryCover}
        onToggleCarousel={libraryActions.toggleCarouselLibrary}
        onReorder={libraryActions.reorderLibraries}
      />
      <LibraryStorageStats />
    </div>
  )
}
