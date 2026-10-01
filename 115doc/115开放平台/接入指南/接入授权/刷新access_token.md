## 刷新access_token

### 基本信息

| 属性         | 内容                               |
|:-----------|:---------------------------------|
| 接口名称       | 刷新access_token                   |
| 接口版本       | v1.0                             |
| 接口路径       | /refreshToken                    |
| 请求方法       | POST                             |
| 接口状态       | 生产环境                             |

### 接口说明

该接口用于通过 `refresh_token` 获取新的 `access_token` 和 `refresh_token`。

### 接口地址

```
https://passportapi.115.com/open/refreshToken
```

### 请求方式

```
POST
Content-Type: application/x-www-form-urlencoded
```

### 认证方式

```
OAuth 2.0 刷新凭证
```

### 请求参数

| 参数名          | 类型     | 必填 | 默认值 | 说明                   | 约束/示例                       |
|:-------------|:-------|:---|:----|:---------------------|:----------------------------|
| refresh_token | string | 是  | -   | 用于刷新访问凭证的刷新凭证        | `REFRESH_TOKEN_PLACEHOLDER` |

### 请求示例

```shell
curl 'https://passportapi.115.com/open/refreshToken' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'refresh_token=REFRESH_TOKEN_PLACEHOLDER'
```

### 响应字段说明

| 字段                 | 类型     | 描述                                  |
|:-------------------|:-------|:------------------------------------|
| state              | int    | 状态码                                 |
| code               | int    | 错误码                                 |
| message            | string | 响应信息                                |
| data               | object | 响应数据                                |
| data.access_token  | string | 新的 `access_token`，同时刷新有效期            |
| data.refresh_token | string | 新的 `refresh_token`，其有效期不延长、不改变       |
| data.expires_in    | int    | `access_token` 有效期，单位为秒               |

### 响应示例

```json
{
  "state": 1,
  "code": 0,
  "message": "",
  "data": {
    "access_token": "ACCESS_TOKEN_PLACEHOLDER",
    "refresh_token": "REFRESH_TOKEN_PLACEHOLDER",
    "expires_in": 7200
  }
}
```

### 注意事项

- `access_token` 有效期为 7200 秒，调用方应以响应中的 `expires_in` 计算刷新时间。
- 同一授权在 60 秒内重复刷新会触发频率控制；多进程或多节点调用方应确保同一授权同一时间只有一个刷新请求。
- 调用后会同时生成新的 `access_token` 和 `refresh_token`。调用方必须将两者作为一组原子保存，并停止使用刷新前的旧凭证；刷新凭证本身的有效期不延长、不改变。
- 收到 `40140125` 或 `40140126` 时，不要使用原 `access_token` 重复重试。应先读取已保存的最新凭证；没有可用新凭证时，再调用本接口刷新。
- `40140116`、`40140119`、`40140120` 是终态错误，用同一个 `refresh_token` 重试永远不会成功。同一 `refresh_token` 连续多次以这三种原因失败会被服务端标记为永久失效，之后每次刷新都直接返回 `40140137`。客户端收到这三种错误或 `40140137` 时必须停止重试，引导用户重新授权；后台常驻程序（如 NAS 同步任务）尤其要实现该逻辑，避免长期无效轮询。
- `access_token` 和 `refresh_token` 属于敏感凭证，不得写入公开仓库、客户端日志或公开沟通内容。

### 修改历史

| 修改时间                         | 修改说明 |
|:-----------------------------|:-----|
| 2025年04月01日(周二) 00:00:00 | 创建文档 |
| 2026年08月10日(周一) 10:23:15 | 修正 `access_token` 有效期示例，补充凭证轮换及并发刷新说明 |
| 2026年08月24日(周一) 10:16:52 | 新增 `refresh_token` 永久失效判定规则与终态错误码 `40140137` 说明 |
