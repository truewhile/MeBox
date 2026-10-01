package model

// NOTE: 原 APIConfig（provider varchar(32) / api_key text）与 api_config.go
// 里的 ApiConfig（provider varchar(64) / api_key varchar(512)）映射到同一张
// api_configs 表，AutoMigrate 每次启动互相改列；且 api_key 被收窄成
// varchar(512) 后，成人区/豆瓣等存的长 AES-GCM Cookie 密文一旦入库，下次
// 启动迁移即失败、服务无法启动。两者已合并为 api_config.go 中唯一的
// APIConfig 结构体（字段取并集），此处不再定义重复模型。
