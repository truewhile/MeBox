// 115 云下载（离线下载）相关类型定义（与后端 model.CloudDownloadTask 对应）。

export type CloudDownloadStatus =
  | 'queued'
  | 'submitted'
  | 'downloading'
  | 'succeeded'
  | 'failed'
  | 'canceled'

export type CloudDownloadStage =
  | 'pending'
  | 'waiting_sync'
  | 'syncing'
  | 'scanning'
  | 'scraping'
  | 'done'
  | 'na'
  | 'failed'

export type CloudDownloadSourceType = 'magnet' | 'torrent' | 'ed2k' | 'http' | 'ftp'

export interface CloudDownloadTask {
  id: string
  account_id: string
  sync_path_id: string
  library_id?: string
  provider: string
  source_type: CloudDownloadSourceType
  source_url: string
  source_key: string
  info_hash?: string
  torrent_name?: string
  torrent_sha1?: string
  pick_code?: string
  wp_path_id: string
  save_path?: string
  status: CloudDownloadStatus
  percent: number
  size_bytes: number
  error: string
  stage: CloudDownloadStage
  stage_error: string
  strm_record_id?: string
  scan_added: number
  scrape_queued: number
  retry_count: number
  created_at: string
  updated_at: string
  completed_at?: string | null
  // 列表接口补充的同步目录信息
  sync_path_name?: string
  remote_display_path?: string
  local_path?: string
}

export interface CloudDownloadList {
  items: CloudDownloadTask[]
  total: number
  page: number
  page_size: number
}

export interface CloudDownloadSubmitItem {
  task_id?: string
  source: string
  ok: boolean
  error?: string
}

export interface CloudDownloadSkipped {
  raw: string
  reason: string
}

export interface CloudDownloadSubmitResult {
  accepted: number
  rejected: number
  items: CloudDownloadSubmitItem[]
  skipped?: CloudDownloadSkipped[]
}

export interface CloudDownloadQuotaPackage {
  surplus: number
  used: number
  count: number
  name: string
}

export interface CloudDownloadQuota {
  package?: CloudDownloadQuotaPackage[]
  count: number
  surplus: number
  used: number
}
