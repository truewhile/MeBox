export interface LibraryRoot {
  id: string
  library_id: string
  name?: string
  path: string
  enabled: boolean
  sort_order: number
  created_at: string
  updated_at: string
}

export interface Library {
  id: string
  name: string
  path: string
  type: string
  cover_url?: string
  enabled: boolean
  sort_order?: number
  carousel_enabled?: boolean
  roots?: LibraryRoot[]
  created_at: string
  updated_at: string
  /** 远程 Emby 挂载库（只读，不支持扫描/刮削/编辑） */
  is_remote_emby?: boolean
  remote_source?: string
  /** 媒体条目总数；`/api/libraries` 已在元数据请求中一并返回。 */
  total?: number
}

export interface ScanResult {
  library_id: string
  visited: number
  added: number
  updated?: number
  probed: number
  local_metadata?: number
  removed?: number
  skipped?: number
  discovered?: number
  queued?: boolean
  cloud?: boolean
  message?: string
  estimate_message?: string
}

export interface Setting {
  key: string
  value: string
  updated_at: string
}

export interface AccessLog {
  id: string
  user_id: string
  action: string
  target: string
  ip: string
  detail: string
  created_at: string
}
