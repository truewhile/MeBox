import { api } from './client'
import type { ScrapeQueueSnapshot } from '../types/scraper'

export interface EnqueueScrapeOptions {
  episode_images?: boolean
  episode_artwork?: boolean
  refresh_matched?: boolean
  include_matched?: boolean
}

export const scraperAPI = {
  queue: (status?: string, page = 1, pageSize = 50) =>
    api
      .get<ScrapeQueueSnapshot>('/admin/scraper/queue', {
        params: { status, page, page_size: pageSize },
      })
      .then((r) => r.data),

  cancelTask: (id: string) =>
    api.post(`/admin/scraper/queue/${id}/cancel`).then((r) => r.data),

  retryTask: (id: string) =>
    api.post(`/admin/scraper/queue/${id}/retry`).then((r) => r.data),

  deleteTask: (id: string) =>
    api.delete(`/admin/scraper/queue/${id}`).then((r) => r.data),

  batchAction: (action: 'delete' | 'retry' | 'cancel', ids: string[]) =>
    api
      .post<{ affected: number; action: string }>('/admin/scraper/queue/batch', { action, ids })
      .then((r) => r.data),

  clearDone: () =>
    api.post<{ deleted: number }>('/admin/scraper/queue/clear-done').then((r) => r.data),

  clearFinished: () =>
    api.post<{ deleted: number }>('/admin/scraper/queue/clear-finished').then((r) => r.data),

  clearCanceled: () =>
    api.post<{ deleted: number }>('/admin/scraper/queue/clear-canceled').then((r) => r.data),

  clearFailed: () =>
    api.post<{ deleted: number }>('/admin/scraper/queue/clear-failed').then((r) => r.data),

  retryFailed: () =>
    api.post<{ retried: number }>('/admin/scraper/queue/retry-failed').then((r) => r.data),

  cancelPending: () =>
    api.post<{ canceled: number }>('/admin/scraper/queue/cancel-pending').then((r) => r.data),

  enqueueLibrary: (libraryId: string, options?: EnqueueScrapeOptions) =>
    api
      .post<{ enqueued: number }>(`/admin/scraper/queue/enqueue-library/${libraryId}`, options ?? {})
      .then((r) => r.data),

  enqueueAll: (options?: EnqueueScrapeOptions) =>
    api
      .post<{ enqueued: number }>('/admin/scraper/queue/enqueue-all', options ?? {})
      .then((r) => r.data),
}
