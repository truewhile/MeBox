import { libraryAPI } from '../api/library'
import type { Library } from '../types'

// 会话级媒体库元数据缓存（不落 localStorage）。
// 首页 / 媒体库页 / 媒体库详情页都要先拿库列表或库信息才能渲染，
// 同一会话内反复进出这些页面时直接命中缓存，避免每次都等一整轮请求。
// TTL 内直接返回缓存；过期后重新请求并刷新。并发调用共享同一个在途请求。

const LIST_TTL_MS = 30_000
const LIBRARY_TTL_MS = 30_000

let listData: Library[] | null = null
let listAt = 0
let listInflight: Promise<Library[]> | null = null

const byId = new Map<string, { at: number; data: Library }>()
const byIdInflight = new Map<string, Promise<Library>>()

function fresh(at: number, ttl: number): boolean {
  return Date.now() - at < ttl
}

function rememberList(data: Library[]) {
  listData = data
  listAt = Date.now()
  for (const lib of data) {
    byId.set(lib.id, { at: listAt, data: lib })
  }
}

// fetchLibraries 拉取库列表：TTL 内命中缓存立即返回（含 peek 先行渲染），
// 过期或未缓存时发起请求，并发调用共享在途请求。
export function fetchLibraries(): Promise<Library[]> {
  if (listData && fresh(listAt, LIST_TTL_MS)) return Promise.resolve(listData)
  if (!listInflight) {
    listInflight = libraryAPI
      .list()
      .then((rows) => {
        const data = Array.isArray(rows) ? rows : []
        rememberList(data)
        return data
      })
      .finally(() => {
        listInflight = null
      })
  }
  return listInflight
}

// peekLibraries 同步返回当前缓存的库列表（可能过期），没有则 null。
// 页面用它先行渲染上一次的数据，再由 fetchLibraries 在后台补一次刷新。
export function peekLibraries(): Library[] | null {
  return listData
}

// peekLibrary 同步返回单库缓存（库列表缓存也会回填单库索引）。
export function peekLibrary(id: string): Library | null {
  const entry = byId.get(id)
  return entry?.data ?? null
}

// resolveLibrary 拿单个库信息：命中缓存直接返回；未命中走 /libraries/:id
// 并回填缓存。远程 Emby 挂载库的详情接口同样返回 is_remote_emby，缓存可用。
export function resolveLibrary(id: string): Promise<Library> {
  const entry = byId.get(id)
  if (entry && fresh(entry.at, LIBRARY_TTL_MS)) return Promise.resolve(entry.data)
  const inflight = byIdInflight.get(id)
  if (inflight) return inflight
  const request = libraryAPI
    .get(id)
    .then((lib) => {
      byId.set(id, { at: Date.now(), data: lib })
      return lib
    })
    .finally(() => {
      byIdInflight.delete(id)
    })
  byIdInflight.set(id, request)
  return request
}

// invalidate 清空缓存。管理页对库做过增删改后调用，确保下次拉到新数据。
export function invalidateLibraries() {
  listData = null
  listAt = 0
  listInflight = null
  byId.clear()
  byIdInflight.clear()
}
