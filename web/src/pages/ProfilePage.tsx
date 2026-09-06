import { FormEvent, useEffect, useState } from 'react'
import toast from 'react-hot-toast'
import { Check, Copy, EyeOff, KeyRound, Loader2, Save, Tv } from 'lucide-react'

import { authAPI } from '../api/auth'
import { profileAPI } from '../api/profile'
import { requestPassword } from '../components/requestPassword'
import { useAuthStore } from '../stores/auth'

export function ProfilePage() {
  const user = useAuthStore((s) => s.user)
  const setUser = useAuthStore((s) => s.setUser)

  const [username, setUsername] = useState(user?.username ?? '')
  const [nickname, setNickname] = useState(user?.nickname ?? '')
  const [email, setEmail] = useState(user?.email ?? '')
  const [avatar, setAvatar] = useState(user?.avatar_url ?? '')
  const [hideAdult, setHideAdult] = useState(Boolean(user?.hide_adult))
  const [oldPwd, setOldPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [savingProfile, setSavingProfile] = useState(false)
  const [savingPassword, setSavingPassword] = useState(false)

  const [otpCode, setOtpCode] = useState('')
  const [otpExpiresIn, setOtpExpiresIn] = useState(0)
  const [generatingOtp, setGeneratingOtp] = useState(false)
  const [copiedOtp, setCopiedOtp] = useState(false)

  useEffect(() => {
    if (otpExpiresIn <= 0) return
    const timer = setInterval(() => {
      setOtpExpiresIn((prev) => {
        if (prev <= 1) {
          clearInterval(timer)
          return 0
        }
        return prev - 1
      })
    }, 1000)
    return () => clearInterval(timer)
  }, [otpExpiresIn])

  useEffect(() => {
    if (window.location.hash === '#tv-otp') {
      const el = document.getElementById('tv-otp')
      if (el) {
        el.scrollIntoView({ behavior: 'smooth' })
      }
    }
  }, [])

  const onGenerateOTP = async () => {
    if (generatingOtp) return
    setGeneratingOtp(true)
    try {
      const res = await authAPI.createTemporaryPassword()
      setOtpCode(res.code)
      setOtpExpiresIn(res.expires_in)
      setCopiedOtp(false)
      toast.success('已生成 6 位临时登录码')
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '生成临时密码失败'
      toast.error(msg)
    } finally {
      setGeneratingOtp(false)
    }
  }

  const copyOTP = async () => {
    if (!otpCode) return
    try {
      await navigator.clipboard.writeText(otpCode)
      setCopiedOtp(true)
      toast.success('已复制到剪贴板')
      setTimeout(() => setCopiedOtp(false), 2000)
    } catch {
      toast.error('复制失败，请手动长按复制')
    }
  }

  const onProfile = async (e: FormEvent) => {
    e.preventDefault()
    if (savingProfile) return
    setSavingProfile(true)
    try {
      let password: string | undefined
      const hideAdultChanged = hideAdult !== Boolean(user?.hide_adult)
      const usernameChanged = username.trim() !== (user?.username ?? '')
      if (hideAdultChanged || usernameChanged) {
        const input = await requestPassword({
          title: usernameChanged ? '修改用户名' : hideAdult ? '隐藏成人目录' : '取消隐藏成人目录',
          message: usernameChanged
            ? '修改用户名后需要使用新用户名登录，请输入当前账号密码确认。'
            : '此设置会同步影响 Web 与 Emby/Jellyfin/Infuse 等第三方客户端，请输入当前账号密码确认。',
          confirmText: '保存设置',
        })
        if (!input) return
        password = input
      }
      const patch: Parameters<typeof profileAPI.update>[0] = {
        username,
        nickname,
        email,
        avatar_url: avatar,
        password,
      }
      if (hideAdultChanged) {
        patch.hide_adult = hideAdult
      }
      const u = await profileAPI.update(patch)
      setUser(u)
      toast.success('资料已更新')
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '保存失败'
      toast.error(msg)
    } finally {
      setSavingProfile(false)
    }
  }

  const onPwd = async (e: FormEvent) => {
    e.preventDefault()
    if (savingPassword) return
    setSavingPassword(true)
    try {
      await authAPI.changePassword(oldPwd, newPwd)
      toast.success('密码已更新')
      setOldPwd('')
      setNewPwd('')
    } catch (err: unknown) {
      const msg =
        (err as { response?: { data?: { error?: string } } })?.response?.data?.error ??
        '密码更新失败'
      toast.error(msg)
    } finally {
      setSavingPassword(false)
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="font-display text-3xl font-bold text-ink-600">个人资料</h1>

      <form onSubmit={onProfile} className="glass-panel space-y-4">
        <h2 className="font-display text-lg font-semibold text-ink-600">基本信息</h2>
        <Field label="用户名">
          <input
            required
            className="input-base"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
          />
        </Field>
        <Field label="昵称">
          <input
            className="input-base"
            value={nickname}
            onChange={(e) => setNickname(e.target.value)}
          />
        </Field>
        <Field label="角色">
          <input className="input-base" value={user?.role ?? ''} disabled />
        </Field>
        <Field label="电子邮箱">
          <input
            type="email"
            className="input-base"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </Field>
        <Field label="头像 URL">
          <input
            className="input-base"
            value={avatar}
            onChange={(e) => setAvatar(e.target.value)}
          />
        </Field>
        <label className="flex items-start justify-between gap-4 rounded-2xl border border-gray-200 bg-white/70 p-4">
          <span>
            <span className="flex items-center gap-2 font-medium text-ink-600">
              <EyeOff size={16} /> 隐藏成人目录
            </span>
            <span className="mt-1 block text-sm leading-6 text-ink-50">
              开启后当前账号在网页、外部播放器链接以及 Emby/Jellyfin/Infuse 等第三方客户端中都不会显示成人媒体库和 NSFW 条目。
            </span>
          </span>
          <input
            type="checkbox"
            className="mt-1 h-5 w-5 accent-brand-500"
            checked={hideAdult}
            onChange={(e) => setHideAdult(e.target.checked)}
          />
        </label>
        <button type="submit" disabled={savingProfile} className="neon-button">
          {savingProfile ? <Loader2 size={16} className="animate-spin" /> : <Save size={16} />}
          保存
        </button>
      </form>

      <form onSubmit={onPwd} className="glass-panel space-y-4">
        <h2 className="font-display text-lg font-semibold text-ink-600">修改密码</h2>
        <Field label="当前密码">
          <input
            required
            type="password"
            className="input-base"
            value={oldPwd}
            onChange={(e) => setOldPwd(e.target.value)}
            autoComplete="current-password"
          />
        </Field>
        <Field label="新密码">
          <input
            required
            type="password"
            className="input-base"
            minLength={6}
            value={newPwd}
            onChange={(e) => setNewPwd(e.target.value)}
            autoComplete="new-password"
          />
        </Field>
        <button type="submit" disabled={savingPassword} className="neon-button">
          {savingPassword ? <Loader2 size={16} className="animate-spin" /> : <KeyRound size={16} />}
          更新密码
        </button>
      </form>

      <section id="tv-otp" className="glass-panel space-y-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 className="font-display text-lg font-semibold text-ink-600 flex items-center gap-2">
              <Tv size={20} className="text-brand-500" />
              Emby / 电视端临时登录码 (OTP)
            </h2>
            <p className="mt-1 text-sm text-ink-50">
              适用于电视盒子、Apple TV、车机或朋友设备上的 Emby / Infuse / Jellyfin 客户端快速登录。无需使用遥控器输入长密码。
            </p>
          </div>
          <button
            type="button"
            onClick={onGenerateOTP}
            disabled={generatingOtp}
            className="neon-button shrink-0"
          >
            {generatingOtp ? <Loader2 size={16} className="animate-spin" /> : <KeyRound size={16} />}
            {otpCode && otpExpiresIn > 0 ? '重新生成' : '获取 6 位临时登录码'}
          </button>
        </div>

        {otpCode && otpExpiresIn > 0 ? (
          <div className="rounded-2xl border border-brand-200 bg-brand-50/40 p-4 space-y-3">
            <div className="flex items-center justify-between text-xs text-ink-50">
              <div>
                <span>登录账号：</span>
                <span className="font-mono font-bold text-ink-600">{user?.username}</span>
              </div>
              <div>
                <span>有效时间剩余：</span>
                <span className={`font-mono font-bold ${otpExpiresIn < 60 ? 'text-red-500' : 'text-brand-600'}`}>
                  {Math.floor(otpExpiresIn / 60).toString().padStart(2, '0')}:{(otpExpiresIn % 60).toString().padStart(2, '0')}
                </span>
              </div>
            </div>

            <div className="flex items-center justify-between rounded-xl bg-white/90 p-4 border border-brand-100 shadow-sm">
              <div className="font-mono text-3xl font-black tracking-widest text-brand-600 select-all">
                {otpCode}
              </div>
              <button
                type="button"
                onClick={copyOTP}
                className="flex items-center gap-1.5 rounded-lg bg-brand-100 px-3 py-1.5 text-xs font-semibold text-brand-700 hover:bg-brand-200 transition-colors"
              >
                {copiedOtp ? <Check size={14} /> : <Copy size={14} />}
                {copiedOtp ? '已复制' : '复制密码'}
              </button>
            </div>

            <p className="text-xs text-ink-50 leading-relaxed">
              在客户端的 Emby / Jellyfin 登录界面输入用户名 <code className="font-bold text-ink-600">{user?.username}</code> 和上方 6 位数字临时密码即可完成登录。登录成功后临时密码立即作废（一次性使用），客户端将自动换取长期持久令牌。
            </p>
          </div>
        ) : null}
      </section>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="mb-1 block text-sm text-ink-100">{label}</span>
      {children}
    </label>
  )
}
