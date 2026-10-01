## 添加云下载BT任务

### 基本信息

| 属性          | 内容                              |
|:-------------|:----------------------------------|
| 接口名称      | 添加云下载BT任务                   |
| 接口版本      | v1.0                              |
| 接口路径      | /add_task_bt                      |
| 请求方法      | POST                              |
| 接口状态      | 生产环境                           |

### 接口说明

根据已解析的BT种子信息添加云下载BT任务。

### 接口地址

```
https://proapi.115.com/open/offline/add_task_bt
```

### 请求方式

```
POST
Content-Type: multipart/form-data
```

### 认证方式

```
Authorization: Bearer access_token
```

### 请求参数

| 参数名       | 类型   | 必填 | 默认值 | 说明                              | 约束/示例        |
|:-------------|:-------|:-----|:-------|:----------------------------------|:----------------|
| info_hash    | string | 是   | -      | BT任务Hash                        | `<info_hash>`    |
| wanted       | string | 是   | -      | 选中下载的文件索引，使用半角逗号分隔 | `<file_indexes>` |
| save_path    | string | 是   | -      | BT任务文件保存路径                 | `A/B`           |
| torrent_sha1 | string | 是   | -      | BT种子SHA1                        | `<torrent_sha1>` |
| pick_code    | string | 是   | -      | BT种子文件提取码                   | `<pick_code>`    |
| wp_path_id   | string | 否   | 0      | 保存目标文件夹ID                   | 0               |

### 请求示例

```shell
curl 'https://proapi.115.com/open/offline/add_task_bt' \
  -H 'Authorization: Bearer <access_token>' \
  --form-string 'info_hash=<info_hash>' \
  --form-string 'wanted=<file_indexes>' \
  --form-string 'save_path=A/B' \
  --form-string 'torrent_sha1=<torrent_sha1>' \
  --form-string 'pick_code=<pick_code>' \
  --form-string 'wp_path_id=0'
```

### 响应字段说明

| 字段    | 类型    | 描述                  |
|:--------|:--------|:----------------------|
| state   | boolean | 操作结果状态            |
| message | string  | 返回信息               |
| code    | int     | 错误码                 |
| data    | object[] | 返回数据，成功时为空数组  |

#### 响应的 code 字段(错误码)说明

| 错误码  | 说明                 | 解决方案                       |
|:--------|:---------------------|:-------------------------------|
| 20018   | 文件不存在或已删除     | 检查提取码、文件归属和种子SHA1  |
| 91006   | 存储空间不足          | 扩充存储空间后重试              |
| 990002  | 参数错误              | 检查必填参数                    |
| 1000012 | 云下载配额已用完       | 购买配额或获得更多配额后重试     |

### 响应示例

```json
{
  "state": true,
  "message": "",
  "code": 0,
  "data": []
}
```

### 业务规则

- `wanted` 可以为 `0`，但不能是空字符串。
- 种子文件需属于当前授权用户、位于指定存储区域，并且文件SHA1与 `torrent_sha1` 一致。
- 不传 `wp_path_id` 时默认保存到根目录；`save_path` 是相对于 `wp_path_id` 所在文件夹的路径。例如，`wp_path_id` 不传或传云下载文件夹ID且 `save_path=A/B` 时，最终路径为根目录下的 `A/B/`。
- 添加任务前会检查当前授权账号的剩余存储空间；空间不足时不扣减云下载配额。
- 云下载服务返回状态码 `10007` 或 `10010` 时，接口统一返回错误码 `1000012`。

### 性能与安全说明

- 用户身份由 Bearer access_token 解析，请求参数不能指定115账号。
- 接口按应用、用户和接口维度执行频率限制。
- 种子文件必须属于当前授权账号，并且文件SHA1与 `torrent_sha1` 一致。

### 注意事项

- 任务处理结果以响应中的 `data` 字段为准。

### 修改历史

| 修改时间                         | 修改说明 |
|:-----------------------------|:-----|
| 2025年04月01日(周二) 00:00:00 | 创建文档 |
