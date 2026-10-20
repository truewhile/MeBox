import { api } from './client'
import type { Media } from '../types'

export type MediaQualityIssue = 'missing' | 'unmatched' | 'poster'

export interface MediaQualityRow {
  issue: MediaQualityIssue
  media: Media
}

export const qualityAPI = {
  list: (issue: MediaQualityIssue, page = 1, pageSize = 50) =>
    api.get<{ items: MediaQualityRow[]; total: number; page: number; page_size: number }>('/admin/quality', {
      params: { issue, page, page_size: pageSize },
    }).then((r) => r.data),
}
