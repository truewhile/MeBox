import { profileAPI } from '../api/profile'

const STORAGE_KEY = 'mebox_pinned_libraries'
const MIGRATION_KEY = 'mebox_pinned_libraries_migrated'

function parsePinnedIds(raw: string | null): string[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.filter((id): id is string => typeof id === 'string' && id.trim() !== '')
  } catch {
    return []
  }
}

export function readLegacyPinnedLibraryIds(): string[] {
  if (typeof window === 'undefined') return []
  return parsePinnedIds(window.localStorage.getItem(STORAGE_KEY))
}

function writeLegacyPinnedLibraryIds(ids: string[]): void {
  if (typeof window === 'undefined') return
  if (ids.length === 0) {
    window.localStorage.removeItem(STORAGE_KEY)
    return
  }
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(ids))
}

function markPinnedLibrariesMigrated(): void {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(MIGRATION_KEY, '1')
  window.localStorage.removeItem(STORAGE_KEY)
}

function hasPinnedLibrariesMigrated(): boolean {
  if (typeof window === 'undefined') return true
  return window.localStorage.getItem(MIGRATION_KEY) === '1'
}

export async function loadPinnedLibraryIds(): Promise<string[]> {
  const remote = await profileAPI.getPinnedLibraries()
  if (!hasPinnedLibrariesMigrated()) {
    const legacy = readLegacyPinnedLibraryIds()
    if (legacy.length > 0 && remote.length === 0) {
      const migrated = await profileAPI.setPinnedLibraries(legacy)
      markPinnedLibrariesMigrated()
      return migrated
    }
    markPinnedLibrariesMigrated()
  }
  return remote
}

export async function savePinnedLibraryIds(ids: string[]): Promise<string[]> {
  const saved = await profileAPI.setPinnedLibraries(ids)
  writeLegacyPinnedLibraryIds(saved)
  return saved
}

export function togglePinnedLibraryId(current: string[], id: string): string[] {
  const index = current.indexOf(id)
  return index >= 0 ? current.filter((item) => item !== id) : [...current, id]
}

export function isLibraryPinned(id: string, pinnedIds: string[]): boolean {
  return pinnedIds.includes(id)
}
