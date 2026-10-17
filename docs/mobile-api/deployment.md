# MeBox Mobile API 接入与部署说明

更新日期：2026-10-17  
适用版本：后端 `feature/android-mobile-api` 分支；App `0.1.0`（versionCode 1）。

## 1. 路径与版本

- 安卓契约挂载在 `/api/v1`，与既有 `/api`、Emby/Jellyfin 入口并行。
- 健康检查：`GET /api/health`。
- 实例身份：`GET /api/v1/server-info`（匿名，`Cache-Control: no-store`）。
- 契约字段与错误码见同目录 `openapi.yaml`。

`server-info.capabilities` 必须反映真实运行能力。当前分支只有
`device_sessions=true`；播放会话、阅读 v1 进度、批量正文与移动端转码仍为
`false`，App 不得据此进入对应流程。

## 2. 连接与传输安全

- 公网必须使用 HTTPS；App 默认使用系统信任链，不提供全局跳过证书校验。
- 局域网 HTTP 需要用户在 App 内明确确认风险；App 只对当前配置的服务器来源
  使用明文，不会把 MeBox `Authorization` 发往其它主机。
- 首版只支持根路径部署。`https://host/mebox` 这类子路径会被 App 明确拒绝，
  不会静默截断为根路径。
- 反代需要保留 `X-Request-ID`（可选）；服务端会回传同值或新生成值，用于
  日志对账。

## 3. 反向代理检查项

以下项用于媒体阶段（M3/M4）回归，当前登录闭环不依赖它们，但仍建议提前配置：

| 项目 | 要求 |
| --- | --- |
| Range / 206 | 透传客户端 `Range`，不要缓存完整响应后改写状态码 |
| 流式响应 | 关闭代理缓冲（如 nginx `proxy_buffering off`） |
| HLS | `.m3u8` 与分片路径不要被改写或强制压缩 |
| WebSocket | 升级头透传，读超时大于心跳间隔 |
| 超时 | 登录/普通 API 30 秒内完成；转码起播超时单独配置 |

目前只验证了普通 JSON 端点；上表其余项尚未在本分支验收。

## 4. 认证与会话

- `POST /api/v1/auth/login` 创建 `auth_sessions` 记录，访问令牌 JWT 增加 `sid`。
- 旧 `/api` 中间件同样校验 `sid`：设备被踢出或退出后，未过期的 JWT 也会立即
  返回 401，不能借旧入口绕过。
- 没有 `sid` 的历史令牌继续按原逻辑访问旧 API；v1 认证端点拒绝这类令牌，
  App 需要重新登录。
- `POST /api/v1/auth/logout` 幂等；会话撤销会同时撤销其名下刷新令牌。
- 设备管理中的踢出操作会同步撤销该设备认证会话。

### 刷新轮换边界

- 同一个刷新令牌的并发请求共享一次轮换；60 秒宽限期内重复提交返回同一结果。
- 轮换结果仍保存在进程内。若新令牌已签发、旧令牌已撤销，但客户端没收到响应
  且服务器随后重启，旧令牌可能无法恢复；此时 App 应要求重新登录。首版接受
  该边界，未实现持久化幂等轮换。
- 会话被撤销时，刷新返回 `401 SESSION_REVOKED`，优先于宽限期复用。

## 5. 错误契约

统一响应：

```json
{
  "code": "SESSION_REVOKED",
  "message": "auth session revoked or expired",
  "request_id": "8a3bade4-cbb3-485b-8e34-2471e4940fc3",
  "retryable": false
}
```

- 401 由 `code` 区分 `TOKEN_EXPIRED`、`SESSION_REVOKED`、`INVALID_CREDENTIALS`。
- 403 表示权限或账号状态，不触发刷新。
- 429 带 `Retry-After`，客户端必须等待。
- 5xx / `SERVICE_UNAVAILABLE` / `retryable=true` 表示可安全重试。

## 6. 数据迁移与回滚

- 新表 `auth_sessions` 与 `refresh_tokens.auth_session_id` 由现有 AutoMigrate
  创建，属于增量变更，不清库。
- SQLite 已验证；PostgreSQL 迁移需要在有实例的环境中单独执行
  `go test ./internal/database ./internal/repository -count=1` 后才能宣称通过。
- 回滚到旧版本不会删除新表；旧程序忽略新增字段。回滚前应先确认不需要再
  依赖 `sid` 即时撤销能力。

## 7. 已知限制

- 单实例部署；未实现多副本共享会话。
- 外部播放令牌尚未继承 `sid`（属于 M3 播放会话范围），当前不能按设备即时撤销。
- 反代子路径、自签名证书信任、多服务器配置均未支持。
- App 目前只开放连接、登录、会话恢复与退出；媒体与阅读入口未实现。
