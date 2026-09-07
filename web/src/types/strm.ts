// STRM 管理相关类型定义（与后端 internal/model/strm.go 对应）。
export type StrmProvider = 'cloud115' | 'clouddrive2' | 'openlist' | 'local' | 'emby_remote'

export const STRM_PROVIDER_LABELS: Record<StrmProvider, string> = {
  cloud115: '115 网盘',
  clouddrive2: 'CloudDrive2',
  openlist: 'OpenList',
  local: '本地目录',
  emby_remote: 'Emby 远程挂载',
}

export type EmbyRemoteLine = {
  name: string
  url: string
}

export interface StrmAccount {
  id: string
  name: string
  provider: StrmProvider
  enabled: boolean
  created_at: string
  updated_at: string
  last_test_at?: string | null
  last_test_result: string
  last_test_ok: boolean
  has_credential: boolean
  provider_label: string
  config_preview?: StrmAccountConfigPreview
  /** 仅远程 Emby 挂载账号返回：播放流量是否经过 MeBox 代理 */
  proxy_play?: boolean
  /** 仅远程 Emby 挂载账号返回：多线路配置 */
  emby_lines?: EmbyRemoteLine[]
  /** 当前优先使用的线路下标 */
  emby_active_line?: number
}

export interface StrmAccountConfigPreview {
  url?: string
  server?: string
  username?: string
  has_password?: boolean
  has_token?: boolean
  has_api_key?: boolean
}

export interface StrmAccountInput {
  name?: string
  provider: StrmProvider
  config?: Record<string, string>
  enabled?: boolean
}

export type StrmSyncStatus = 'idle' | 'running' | 'ok' | 'error' | 'canceled'

export interface StrmSyncPath {
  id: string
  name: string
  account_id: string
  provider: StrmProvider
  remote_path: string
  remote_display_path?: string
  local_path: string
  strm_base_url: string
  video_ext: string
  meta_ext: string
  exclude_name: string
  min_video_size_mb: number
  add_path: number
  download_meta: boolean
  upload_meta: boolean
  delete_dir: boolean
  keep_ext: boolean
  cron: string
  enable_cron: boolean
  sync_mode?: 'incremental' | 'full'
  enabled: boolean
  created_at: string
  last_sync_at?: string | null
  last_sync_status: StrmSyncStatus
  last_sync_message: string
  account_name: string
  account_enabled: boolean
}

export interface StrmSyncPathInput {
  name?: string
  account_id?: string
  provider: StrmProvider
  remote_path: string
  remote_display_path?: string
  local_path: string
  strm_base_url?: string
  video_ext?: string
  meta_ext?: string
  exclude_name?: string
  min_video_size_mb?: number
  add_path?: number
  download_meta?: boolean
  upload_meta?: boolean
  delete_dir?: boolean
  keep_ext?: boolean
  cron?: string
  enable_cron?: boolean
  sync_mode?: 'incremental' | 'full'
  enabled?: boolean
}

export interface StrmSyncRecord {
  id: string
  sync_path_id: string
  sync_type?: 'incremental' | 'full'
  status: 'pending' | 'running' | 'done' | 'failed' | 'canceled'
  total: number
  new_strm: number
  new_meta: number
  uploaded: number
  pruned: number
  skipped: number
  message: string
  started_at?: string | null
  finished_at?: string | null
  created_at: string
}

export type StrmTaskStatus = 'pending' | 'running' | 'done' | 'failed' | 'canceled'

export interface StrmTask {
  id: string
  kind: 'download' | 'upload'
  sync_path_id: string
  account_id: string
  provider: StrmProvider
  file_name: string
  local_path: string
  remote_path: string
  size: number
  status: StrmTaskStatus
  error: string
  retry_count: number
  created_at: string
  started_at?: string | null
  finished_at?: string | null
}

export interface StrmQueueCounts {
  pending: number
  running: number
  done: number
  failed: number
  canceled: number
}

export interface StrmQueueSnapshot {
  counts: StrmQueueCounts
  tasks: StrmTask[]
  total: number // 当前过滤条件下任务总数
  page: number // 当前页码（从 1 开始）
  page_size: number // 单页大小
}

export interface StrmSettingsMap {
  'strm.base_url': string
  'strm.video_ext': string
  'strm.meta_ext': string
  'strm.exclude_name': string
  'strm.min_video_size_mb': string
  'strm.add_path': string
  'strm.download_meta': string
  'strm.upload_meta': string
  'strm.delete_dir': string
  'strm.keep_ext': string
  'strm.download_threads': string
  'strm.upload_threads': string
  [key: string]: string
}

export type StrmTaskStatusFilter = 'all' | StrmTaskStatus