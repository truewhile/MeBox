import { FormEvent, useEffect, useState } from 'react'
import toast from 'react-hot-toast'

import { adminAPI } from '../api/admin'
import type { User } from '../types'
import { confirmAction } from '../components/confirmAction'
import { requestPassword } from '../components/requestPassword'
import { AdminUserLibrariesDialog } from '../components/AdminUserLibrariesDialog'
import { AdminUserDevicesDialog } from './AdminUserDevicesDialog'
import { AdminUsersForm } from './AdminUsersForm'
import { AdminUsersTable } from './AdminUsersTable'
import { ActiveUsersStrip } from './ActiveUsersStrip'

const DEFAULT_MAX_USERS = 20

export function AdminUsersPanel() {
  const [users, setUsers] = useState<User[]>([])
  const [maxUsers, setMaxUsers] = useState(DEFAULT_MAX_USERS)
  const [maxUsersDraft, setMaxUsersDraft] = useState(String(DEFAULT_MAX_USERS))
  const [savingLimit, setSavingLimit] = useState(false)
  const [creating, setCreating] = useState(false)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [editingID, setEditingID] = useState<string | null>(null)
  const [editingUsername, setEditingUsername] = useState('')
  const [resettingPasswordID, setResettingPasswordID] = useState<string | null>(null)
  const [configuringLibrariesUser, setConfiguringLibrariesUser] = useState<User | null>(null)
  const [managingDevicesUser, setManagingDevicesUser] = useState<User | null>(null)

  const refresh = async () => {
    const data = await adminAPI.listUsers()
    setUsers(data.users)
    setMaxUsers(data.max_users)
    setMaxUsersDraft(String(data.max_users))
  }
  useEffect(() => {
    refresh().catch(() => undefined)
    const timer = window.setInterval(() => {
      if (document.hidden) return
      refresh().catch(() => undefined)
    }, 10000)
    return () => window.clearInterval(timer)
  }, [])

  const userLimitReached = users.length >= maxUsers

  const handleSaveMaxUsers = async () => {
    const next = Number(maxUsersDraft)
    if (!Number.isFinite(next) || next < 1) {
      toast.error('用户上限至少为 1')
      return
    }
    if (next > 10000) {
      toast.error('用户上限不能超过 10000')
      return
    }
    if (next === maxUsers) return
    setSavingLimit(true)
    try {
      const data = await adminAPI.updateUserLimit(next)
      setMaxUsers(data.max_users)
      setMaxUsersDraft(String(data.max_users))
      toast.success(`用户上限已更新为 ${data.max_users} 人`)
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '保存用户上限失败'
      toast.error(msg)
    } finally {
      setSavingLimit(false)
    }
  }

  const handleCreate = async (e: FormEvent) => {
    e.preventDefault()
    if (creating) return
    setCreating(true)
    try {
      await adminAPI.createUser({ username, password })
      toast.success('用户已添加，默认仅允许浏览与播放媒体')
      setUsername('')
      setPassword('')
    } catch (err: unknown) {
      const msg =
        userCreateErrorMessage(err) ??
        '添加用户失败'
      toast.error(msg)
    } finally {
      setCreating(false)
    }
    await refresh().catch(() => undefined)
  }

  const startEdit = (u: User) => {
    setEditingID(u.id)
    setEditingUsername(u.username)
  }

  const saveEdit = async (id: string) => {
    try {
      await adminAPI.updateUser(id, { username: editingUsername })
      toast.success('用户名已更新')
      setEditingID(null)
      await refresh()
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '更新失败'
      toast.error(msg)
    }
  }

  const resetPassword = async (u: User) => {
    if (resettingPasswordID) return
    const nextPassword = await requestPassword({
      title: `重置 ${u.username} 的密码`,
      message: '请输入新的临时密码，至少 6 位。保存后该用户可立即使用新密码登录 Web、Bot 与第三方客户端。',
      confirmText: '重置密码',
    })
    if (!nextPassword) return
    if (nextPassword.length < 6) {
      toast.error('新密码至少 6 位')
      return
    }
    setResettingPasswordID(u.id)
    try {
      await adminAPI.resetUserPassword(u.id, nextPassword)
      toast.success('密码已重置')
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '重置密码失败'
      toast.error(msg)
    } finally {
      setResettingPasswordID(null)
    }
  }

  const toggleStatus = async (u: User) => {
    const next = !u.is_active
    if (!next && u.is_protected) {
      toast.error('受保护管理员不可禁用')
      return
    }
    if (
      !next &&
      !(await confirmAction({
        title: '禁用用户',
        message: `禁用「${u.username}」后，Web 与第三方客户端已有登录也会失效。`,
        confirmText: '禁用',
      }))
    ) {
      return
    }
    try {
      await adminAPI.setUserStatus(u.id, next)
      toast.success(next ? '用户已解禁' : '用户已禁用')
      await refresh()
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '操作失败'
      toast.error(msg)
    }
  }

  const deleteUser = async (u: User) => {
    if (u.is_protected) return
    if (!(await confirmAction({ title: '删除用户', message: `确定删除「${u.username}」?`, confirmText: '删除' }))) return
    await adminAPI.deleteUser(u.id)
    toast.success('已删除')
    await refresh()
  }

  return (
    <div className="space-y-6">
      <AdminUsersForm
        usersCount={users.length}
        maxUsers={maxUsers}
        maxUsersDraft={maxUsersDraft}
        savingLimit={savingLimit}
        creating={creating}
        username={username}
        password={password}
        userLimitReached={userLimitReached}
        onMaxUsersDraftChange={setMaxUsersDraft}
        onSaveMaxUsers={handleSaveMaxUsers}
        onUsernameChange={setUsername}
        onPasswordChange={setPassword}
        onSubmit={handleCreate}
      />

      <ActiveUsersStrip users={users} />

      <AdminUsersTable
        users={users}
        editingID={editingID}
        editingUsername={editingUsername}
        resettingPasswordID={resettingPasswordID}
        onEditingUsernameChange={setEditingUsername}
        onSaveEdit={saveEdit}
        onCancelEdit={() => setEditingID(null)}
        onStartEdit={startEdit}
        onResetPassword={resetPassword}
        onConfigureLibraries={(u) => setConfiguringLibrariesUser(u)}
        onManageDevices={(u) => setManagingDevicesUser(u)}
        onToggleStatus={toggleStatus}
        onDeleteUser={deleteUser}
      />

      <AdminUserLibrariesDialog
        user={configuringLibrariesUser}
        isOpen={Boolean(configuringLibrariesUser)}
        onClose={() => setConfiguringLibrariesUser(null)}
        onSaved={async (updated) => {
          setUsers((prev) => prev.map((u) => (u.id === updated.id ? updated : u)))
          await refresh()
        }}
      />

      <AdminUserDevicesDialog
        user={managingDevicesUser}
        isOpen={Boolean(managingDevicesUser)}
        onClose={() => setManagingDevicesUser(null)}
      />
    </div>
  )
}

function userCreateErrorMessage(err: unknown): string | undefined {
  const data = (err as { response?: { data?: { error?: string; max_users?: number } } })?.response?.data
  if (!data?.error) return undefined
  if (data.error === 'user limit reached' && data.max_users != null) {
    return `用户数量已达到上限：${data.max_users} 人`
  }
  return data.error
}
