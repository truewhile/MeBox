import type { Library } from '../types/library.ts'

const TTL_MS = 30_000

// Kept independent of the API and auth stores so races can be tested with
// deferred requests. Only the active visibility scope is retained in memory.
export function createLibraryCache({
  list,
  get,
  readScope,
}: {
  list: () => Promise<Library[]>
  get: (id: string) => Promise<Library>
  readScope: () => string
}) {
  let scope = readScope()
  let generation = 0
  let listData: Library[] | null = null
  let listAt = 0
  let listInflight: Promise<Library[]> | null = null
  const byId = new Map<string, { at: number; data: Library }>()
  const byIdInflight = new Map<string, Promise<Library>>()

  function invalidate() {
    generation += 1
    listData = null
    listAt = 0
    listInflight = null
    byId.clear()
    byIdInflight.clear()
  }

  function syncScope() {
    const next = readScope()
    if (scope !== next) {
      scope = next
      invalidate()
    }
  }

  function checkGeneration(expected: number) {
    syncScope()
    if (generation !== expected) {
      const error = new Error('Library request belongs to an invalidated cache')
      error.name = 'AbortError'
      throw error
    }
  }

  function fetchLibraries(): Promise<Library[]> {
    syncScope()
    if (listData && Date.now() - listAt < TTL_MS) return Promise.resolve(listData)
    if (listInflight) return listInflight
    const expected = generation
    const request = list()
      .then((rows) => {
        checkGeneration(expected)
        const data = Array.isArray(rows) ? rows : []
        listData = data
        listAt = Date.now()
        byId.clear()
        for (const lib of data) byId.set(lib.id, { at: listAt, data: lib })
        return data
      })
      .finally(() => {
        if (listInflight === request) listInflight = null
      })
    listInflight = request
    return request
  }

  function resolveLibrary(id: string): Promise<Library> {
    syncScope()
    const entry = byId.get(id)
    if (entry && Date.now() - entry.at < TTL_MS) return Promise.resolve(entry.data)
    const inflight = byIdInflight.get(id)
    if (inflight) return inflight
    const expected = generation
    const request = get(id)
      .then((lib) => {
        checkGeneration(expected)
        byId.set(id, { at: Date.now(), data: lib })
        return lib
      })
      .finally(() => {
        if (byIdInflight.get(id) === request) byIdInflight.delete(id)
      })
    byIdInflight.set(id, request)
    return request
  }

  return {
    fetchLibraries,
    resolveLibrary,
    invalidate,
    syncScope,
    peekLibraries: () => {
      syncScope()
      return listData
    },
    peekLibrary: (id: string) => {
      syncScope()
      return byId.get(id)?.data ?? null
    },
  }
}
