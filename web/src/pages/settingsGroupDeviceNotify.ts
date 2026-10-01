import type { SettingGroup } from './settingsGroupTypes'

// 设备管控与通知设置。
//
// 这些键原先只能通过 Telegram Bot 命令调整，网页端没有任何入口；本分组把它们
// 暴露成可维护的表单，并解释每个值的实际作用。
export const deviceNotifySettingsGroup: SettingGroup = {
  key: 'device-notify',
  label: '设备与通知',
  description: '登录设备管控、防共享策略与 Telegram 通知通道',
  items: [
    {
      key: 'telegram.enabled',
      label: '启用 Telegram 通知',
      type: 'toggle',
      defaultValue: 'false',
      hint: '总开关。关闭时所有通知静默跳过，不影响任何业务流程。',
    },
    {
      key: 'telegram.bot_token',
      label: 'Bot Token',
      type: 'text',
      placeholder: '123456:AA...',
      hint: 'BotFather 发放的 Token。保存后再次打开会显示为脱敏值；保持脱敏值不动即表示不修改。',
    },
    {
      key: 'telegram.admin_chat_id',
      label: '管理员 Chat ID',
      type: 'text',
      placeholder: '例如 123456789',
      hint: '接收运维通知（任务失败、设备策略动作）的会话 ID。可先用 Bot 给管理员发消息，再从日志或 getUpdates 获取。',
    },
    {
      key: 'device.antishare_enabled',
      label: '启用防共享',
      type: 'toggle',
      defaultValue: 'false',
      hint: '开启后，超过并发播放/登录终端上限会禁用账号；设备指纹变化按警告累计处理。默认关闭。',
    },
    {
      key: 'device.max_concurrent_play',
      label: '最大并发播放设备',
      type: 'number',
      defaultValue: '3',
      hint: '同一账号在判定窗口内同时在线的播放设备数上限。',
    },
    {
      key: 'device.max_logged_clients',
      label: '最大同时登录终端',
      type: 'number',
      defaultValue: '3',
      hint: '活跃天数窗口内允许的登录终端数量上限。',
    },
    {
      key: 'device.warn_threshold',
      label: '设备指纹警告阈值',
      type: 'number',
      defaultValue: '2',
      hint: '设备指纹异常累计超过该次数后禁用账号。警告会同步通知用户本人。',
    },
    {
      key: 'device.play_window_seconds',
      label: '并发判定窗口（秒）',
      type: 'number',
      defaultValue: '90',
      hint: '统计「同时播放」时回看的时间窗口。',
    },
    {
      key: 'device.client_active_days',
      label: '登录设备活跃天数',
      type: 'number',
      defaultValue: '30',
      hint: '统计「同时登录终端」时回看的天数窗口。',
    },
  ],
}
