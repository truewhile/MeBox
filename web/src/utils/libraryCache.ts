import { libraryAPI } from '../api/library'
import { useAuthStore } from '../stores/auth'
import { usePlayProfileStore } from '../stores/playProfile'
import { createLibraryCache } from './libraryCacheState'

export function libraryCacheScope(): string {
  const { token, user } = useAuthStore.getState()
  const profile = usePlayProfileStore.getState()
  return JSON.stringify([
    Boolean(token),
    user?.id,
    user?.role,
    user?.hide_adult,
    user?.allowed_library_ids,
    profile.activeProfileId,
    profile.activeProfilePinToken,
  ])
}

const cache = createLibraryCache({
  list: () => libraryAPI.list(),
  get: (id) => libraryAPI.get(id),
  readScope: libraryCacheScope,
})

// Invalidate synchronously, including logout/login between two reads and auth
// rehydration from another tab. Refreshing a token alone preserves the cache.
useAuthStore.subscribe(() => cache.syncScope())
usePlayProfileStore.subscribe(() => cache.syncScope())

export const fetchLibraries = cache.fetchLibraries
export const peekLibraries = cache.peekLibraries
export const peekLibrary = cache.peekLibrary
export const resolveLibrary = cache.resolveLibrary
export const invalidateLibraries = cache.invalidate
