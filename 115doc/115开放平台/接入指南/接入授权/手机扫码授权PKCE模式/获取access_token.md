## 获取access_token

### 基本信息

| 属性         | 内容                               |
|:-----------|:---------------------------------|
| 接口名称       | 获取access_token                   |
| 接口版本       | v1.0                             |
| 接口路径       | /deviceCodeToToken               |
| 请求方法       | POST                             |
| 接口状态       | 生产环境                             |

### 接口说明

该接口用于在用户确认手机扫码授权后，使用设备码和 PKCE 原始校验值换取 `access_token`。

### 接口地址

```
https://passportapi.115.com/open/deviceCodeToToken
```

### 请求方式

```
POST
Content-Type: application/x-www-form-urlencoded
```

### 认证方式

```
OAuth 2.0 + PKCE
```

### 请求参数

| 参数名        | 类型     | 必填 | 默认值 | 说明                              | 约束/示例                                                          |
|:-----------|:-------|:---|:----|:--------------------------------|:---------------------------------------------------------------|
| uid        | string | 是  | -   | 二维码 ID/设备码                      | `DEVICE_CODE_PLACEHOLDER`                                      |
| code_verifier | string | 是  | -   | 计算 `code_challenge` 时使用的原始随机字符串 | `IGKN6CJanWxCDPDhHZJrhswQdlcPBGLqExkhyujysXaQ4fJKBk_6dlPJo47s` |

### 请求示例

```shell
curl 'https://passportapi.115.com/open/deviceCodeToToken' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'uid=DEVICE_CODE_PLACEHOLDER' \
  --data-urlencode 'code_verifier=IGKN6CJanWxCDPDhHZJrhswQdlcPBGLqExkhyujysXaQ4fJKBk_6dlPJo47s'
```

### 响应字段说明

| 字段                 | 类型     | 描述                              |
|:-------------------|:-------|:--------------------------------|
| state              | int    | 状态码                             |
| code               | int    | 错误码                             |
| message            | string | 响应信息                            |
| data               | object | 响应数据                            |
| data.access_token  | string | 访问资源接口的凭证                       |
| data.refresh_token | string | 刷新 `access_token` 的凭证，有效期 1 年     |
| data.expires_in    | int    | `access_token` 有效期，单位为秒           |

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

- `code_verifier` 必须与生成 `code_challenge` 时使用的原始值一致。
- `access_token` 和 `refresh_token` 属于敏感凭证，不得写入公开仓库、客户端日志或公开沟通内容。

### 修改历史

| 修改时间                         | 修改说明 |
|:-----------------------------|:-----|
| 2025年04月01日(周二) 00:00:00 | 创建文档 |
