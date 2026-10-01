## 用授权码换取access_token

### 基本信息

| 属性         | 内容                               |
|:-----------|:---------------------------------|
| 接口名称       | 用授权码换取access_token                |
| 接口版本       | v1.0                             |
| 接口路径       | /authCodeToToken                 |
| 请求方法       | POST                             |
| 接口状态       | 生产环境                             |

### 接口说明

该接口用于通过授权码换取 `access_token`。建议在开发者服务端调用，避免泄露 AppSecret。

### 接口地址

```
https://passportapi.115.com/open/authCodeToToken
```

### 请求方式

```
POST
Content-Type: application/x-www-form-urlencoded
```

### 认证方式

```
OAuth 2.0 授权码模式
```

### 请求参数

| 参数名          | 类型     | 必填 | 默认值 | 说明                                  | 约束/示例                         |
|:-------------|:-------|:---|:----|:------------------------------------|:------------------------------|
| client_id    | string | 是  | -   | AppID                               | `YOUR_APP_ID`                 |
| client_secret | string | 是  | -   | AppSecret                           | `YOUR_APP_SECRET`             |
| code         | string | 是  | -   | 请求授权接口重定向返回的授权码                    | `AUTHORIZATION_CODE_PLACEHOLDER` |
| redirect_uri | string | 是  | -   | 与请求授权时传入的 `redirect_uri` 一致，用于防止 MITM 和 CSRF 攻击 | `https://foo.com?state=123456` |
| grant_type   | string | 是  | -   | 授权类型，固定为 `authorization_code`         | `authorization_code`          |

### 请求示例

```shell
curl 'https://passportapi.115.com/open/authCodeToToken' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'client_id=YOUR_APP_ID' \
  --data-urlencode 'client_secret=YOUR_APP_SECRET' \
  --data-urlencode 'code=AUTHORIZATION_CODE_PLACEHOLDER' \
  --data-urlencode 'redirect_uri=https://foo.com?state=123456' \
  --data-urlencode 'grant_type=authorization_code'
```

### 响应字段说明

| 字段                 | 类型     | 描述                              |
|:-------------------|:-------|:--------------------------------|
| state              | int    | 状态码：0-失败；1-成功                   |
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

- 必须在服务端安全保存并使用 AppSecret，不得在客户端代码、公开仓库或日志中泄露。
- `redirect_uri` 必须与请求授权时传入的值一致。
- `access_token` 和 `refresh_token` 属于敏感凭证，应按照敏感数据规范存储。

### 修改历史

| 修改时间                         | 修改说明 |
|:-----------------------------|:-----|
| 2025年04月01日(周二) 00:00:00 | 创建文档 |
