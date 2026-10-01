## 解析BT种子

### 基本信息

| 属性          | 内容                              |
|:-------------|:----------------------------------|
| 接口名称      | 解析BT种子                         |
| 接口版本      | v1.0                              |
| 接口路径      | /torrent                          |
| 请求方法      | POST                              |
| 接口状态      | 生产环境                           |

### 接口说明

解析已上传的BT种子文件，返回种子任务信息和文件列表。

### 接口地址

```
https://proapi.115.com/open/offline/torrent
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

| 参数名       | 类型   | 必填 | 默认值 | 说明           | 约束/示例        |
|:-------------|:-------|:-----|:-------|:---------------|:----------------|
| torrent_sha1 | string | 是   | -      | BT种子文件SHA1 | `<torrent_sha1>` |
| pick_code    | string | 是   | -      | BT种子文件提取码 | `<pick_code>`    |

### 请求示例

```shell
curl 'https://proapi.115.com/open/offline/torrent' \
  -H 'Authorization: Bearer <access_token>' \
  --form-string 'torrent_sha1=<torrent_sha1>' \
  --form-string 'pick_code=<pick_code>'
```

### 响应字段说明

| 字段                           | 类型     | 描述                 |
|:-------------------------------|:---------|:---------------------|
| state                          | boolean  | 状态，true表示成功     |
| message                        | string   | 返回信息              |
| code                           | int      | 错误码                |
| data                           | object   | 种子解析结果           |
| data.file_size                 | int      | 任务大小              |
| data.torrent_name              | string   | 任务名称              |
| data.file_count                | int      | 文件数量              |
| data.info_hash                 | string   | 任务SHA1              |
| data.torrent_filelist          | object[] | 文件列表              |
| data.torrent_filelist[].size   | int      | 文件大小              |
| data.torrent_filelist[].path   | string   | 文件路径              |
| data.torrent_filelist[].wanted | int      | 文件是否默认选中       |

#### 响应的 code 字段(错误码)说明

| 错误码 | 说明                 | 解决方案                         |
|:-------|:---------------------|:---------------------------------|
| 20018  | 文件不存在或已删除     | 检查提取码、文件归属和种子SHA1    |
| 990002 | 参数错误             | 检查必填参数                     |

### 响应示例

```json
{
  "state": true,
  "message": "",
  "code": 0,
  "data": {
    "file_size": 0,
    "torrent_name": "",
    "file_count": 0,
    "info_hash": "",
    "torrent_filelist": [
      {
        "size": 0,
        "path": "",
        "wanted": 0
      }
    ]
  }
}
```

### 业务规则

- 种子文件需属于当前授权用户、位于指定存储区域，并且文件SHA1与 `torrent_sha1` 一致。
- 现有开放平台文档建议先将种子文件上传至“云下载/种子文件”文件夹，但该目录不是硬性要求。

### 性能与安全说明

- 用户身份由 Bearer access_token 解析，请求参数不能指定115账号。
- 接口按应用、用户和接口维度执行频率限制。
- 种子文件必须属于当前授权账号，并且文件SHA1与 `torrent_sha1` 一致。

### 注意事项

- 种子解析结果以响应中的 `data` 字段为准。

### 修改历史

| 修改时间                         | 修改说明 |
|:-----------------------------|:-----|
| 2025年04月01日(周二) 00:00:00 | 创建文档 |
