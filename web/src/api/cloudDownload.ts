// 115 云下载 API。
import { LONG_REQUEST_TIMEOUT, api } from './client'
import type {
  CloudDownloadList,
  CloudDownloadQuota,
  CloudDownloadSubmitResult,
} from '../types/cloudDownload'

export interface CloudDownloadListParams {
  status?: string
  sync_path_id?: string
  account_id?: string
  page?: number
  page_size?: number
}

export const cloudDownloadAPI = {
  list: (params: CloudDownloadListParams = {}) =>
    api.get<CloudDownloadList>('/admin/cloud-downloads', { params }).then((r) => r.data),

  /** 提交一批链接（磁力 / ed2k / http / ftp）。部分失败也在 200 的 items 里返回。 */
  submit: (payload: { sync_path_id: string; urls: string[] }) =>
    api
      .post<CloudDownloadSubmitResult>('/admin/cloud-downloads', payload, { timeout: LONG_REQUEST_TIMEOUT })
      .then((r) => r.data),

  /** 上传 .torrent 并提交为 BT 任务。 */
  submitTorrent: (file: File, syncPathID: string) => {
    const form = new FormData()
    form.append('file', file)
    form.append('sync_path_id', syncPathID)
    return api
      .post<CloudDownloadSubmitResult>('/admin/cloud-downloads/torrent', form, {
        timeout: LONG_REQUEST_TIMEOUT,
        headers: { 'Content-Type': 'multipart/form-data' },
      })
      .then((r) => r.data)
  },

  quota: (accountID: string) =>
    api
      .get<CloudDownloadQuota>('/admin/cloud-downloads/quota', { params: { account_id: accountID } })
      .then((r) => r.data),

  retry: (id: string) => api.post(`/admin/cloud-downloads/${id}/retry`).then((r) => r.data),

  retryPipeline: (id: string) =>
    api.post(`/admin/cloud-downloads/${id}/retry-pipeline`).then((r) => r.data),

  cancel: (id: string) => api.post(`/admin/cloud-downloads/${id}/cancel`).then((r) => r.data),

  remove: (id: string) => api.delete(`/admin/cloud-downloads/${id}`).then((r) => r.data),

  clearFinished: () =>
    api.post<{ deleted: number }>('/admin/cloud-downloads/clear-finished').then((r) => r.data)
}
