import { api, MIGRATION_REQUEST_TIMEOUT } from './client'
import type { AccessLog, Setting, User } from '../types'

export interface DatabaseStatus {
  type: 'sqlite' | 'postgres'
  dsn?: string
  db_path?: string
  open_conns: number
  in_use: number
  idle: number
  max_open_conns: number
  table_counts?: Record<string, number>
}

export interface PostgresTestResult {
  success: boolean
  latency_ms?: number
  version?: string
  message?: string
  error?: string
}

export interface DatabaseMigrationResult {
  success: boolean
  total_rows: number
  table_rows?: Record<string, number>
  duration_ms: number
  message?: string
  error?: string
}

export interface DatabaseConnectionPayload {
  type?: string
  dsn?: string
  host?: string
  port?: number
  user?: string
  password?: string
  dbname?: string
  sslmode?: string
}

export interface SystemUpdateStatus {
  image: string
  current_version?: string
  watchtower_image?: string
  update_mode?: string
  compose_dir?: string
  compose_file?: string
  compose_command?: string
  container_id?: string
  container_name?: string
  current_image_id?: string
  local_digest?: string
  remote_digest?: string
  docker_available: boolean
  can_apply: boolean
  update_available?: boolean
  running: boolean
  task_id?: string
  message?: string
  details?: string
  checked_at?: string
  started_at?: string
}

export interface UserListResponse {
  users: User[]
  max_users: number
  current_users: number
}

export interface UserLimitResponse {
  max_users: number
  current_users: number
}

export const adminAPI = {
  listUsers: () => api.get<UserListResponse>('/admin/users').then((r) => r.data),

  getUserLimit: () => api.get<UserLimitResponse>('/admin/users/limit').then((r) => r.data),

  updateUserLimit: (maxUsers: number) =>
    api.put<UserLimitResponse>('/admin/users/limit', { max_users: maxUsers }).then((r) => r.data),

  createUser: (payload: { username: string; password: string }) =>
    api.post<User>('/admin/users', payload).then((r) => r.data),

  updateUser: (id: string, payload: { username: string }) =>
    api.patch<User>(`/admin/users/${id}`, payload).then((r) => r.data),

  resetUserPassword: (id: string, password: string) =>
    api.patch(`/admin/users/${id}/password`, { password }).then((r) => r.data),

  setUserStatus: (id: string, isActive: boolean) =>
    api.patch<User>(`/admin/users/${id}/status`, { is_active: isActive }).then((r) => r.data),

  updateUserLibraries: (id: string, allowedLibraryIDs: string[] | null) =>
    api.patch<User>(`/admin/users/${id}/libraries`, { allowed_library_ids: allowedLibraryIDs }).then((r) => r.data),

  deleteUser: (id: string) => api.delete(`/admin/users/${id}`).then((r) => r.data),

  listSettings: () => api.get<Setting[]>('/admin/settings').then((r) => r.data),

  updateSetting: (key: string, value: string) =>
    api.put('/admin/settings', { key, value }).then((r) => r.data),

  recentLogs: () => api.get<AccessLog[]>('/admin/logs').then((r) => r.data),

  systemUpdateStatus: () => api.get<SystemUpdateStatus>('/admin/system/update').then((r) => r.data),

  systemUpdateCheck: () => api.post<SystemUpdateStatus>('/admin/system/update/check').then((r) => r.data),

  systemUpdateApply: () => api.post<SystemUpdateStatus>('/admin/system/update/apply').then((r) => r.data),

	  testAdultScraper: (payload: {
	    engine?: string
	    server_url?: string
	    token?: string
	    javdb_url?: string
	    javbus_url?: string
	    cookie?: string
	  }) =>
	    api
	      .post<{
	        success: boolean
	        latency_ms?: number
	        providers?: string[]
	        message?: string
	        error?: string
	      }>('/admin/adult/test-scraper', payload)
	      .then((r) => r.data),

	  getDatabaseStatus: () =>
	    api.get<DatabaseStatus>('/admin/database/status').then((r) => r.data),

	  testDatabaseConnection: (payload: DatabaseConnectionPayload) =>
	    api.post<PostgresTestResult>('/admin/database/test', payload).then((r) => r.data),

  migrateDatabase: (payload: DatabaseConnectionPayload) =>
    api
      .post<DatabaseMigrationResult>('/admin/database/migrate', payload, {
        timeout: MIGRATION_REQUEST_TIMEOUT,
      })
      .then((r) => r.data),

	  saveDatabaseConfig: (payload: DatabaseConnectionPayload) =>
	    api.post<{ message: string; type: string }>('/admin/database/save-config', payload).then((r) => r.data),
	}
