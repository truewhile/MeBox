# MeBox

<p align="center">
  <img src="web/public/brand/logo-192.png" width="96" height="96" alt="MeBox Logo" />
</p>

<h3 align="center">面向 NAS 与家庭影音场景的私人媒体中心</h3>

<p align="center">
  <strong>媒体库 · 刮削整理 · 网盘 STRM · 兼容 Emby/Jellyfin 客户端 · 远程 Emby 挂载 · 多用户权限 · Docker 一键部署</strong>
</p>

<p align="center">
  <a href="#项目简介">项目简介</a> ·
  <a href="#快速开始">快速开始</a> ·
  <a href="#部署档位">部署档位</a> ·
  <a href="#鸣谢">鸣谢</a> ·
  <a href="#开发构建">开发构建</a> ·
  <a href="README_EN.md">English</a> ·
  <a href="CONTRIBUTING.md">贡献规范</a> ·
  <a href="https://t.me/MeBoxGroup">Telegram 群组</a>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat-square&logo=go&logoColor=white" />
  <img alt="React" src="https://img.shields.io/badge/React-18-61DAFB?style=flat-square&logo=react&logoColor=111827" />
  <img alt="Docker" src="https://img.shields.io/badge/Docker-ready-2496ED?style=flat-square&logo=docker&logoColor=white" />
  <img alt="License" src="https://img.shields.io/badge/License-GPL--3.0-blue?style=flat-square" />
</p>

---

## 项目简介

**MeBox** 是一个自托管私人媒体管理系统，适合 NAS、小主机、家庭共享和多端播放场景。本项目由 [MediaStationGo](https://github.com/ShukeBta/MediaStationGo) fork 并持续二开维护，在保留「一套服务覆盖网页、手机、电视与第三方播放器」思路的同时，围绕网盘播放、任务队列、远程挂载和权限体系做了大量增强。

你可以把 MeBox 理解为：

- 一个带现代 Web UI 的**媒体库后台**
- 一个兼容 Emby/Jellyfin 客户端的**协议网关**
- 一个连接本地硬盘、下载目录与网盘存储的**整理与播放入口**

### 核心能力

| 模块 | 说明 |
| --- | --- |
| **媒体库** | 电影、电视剧、动漫、综艺、音乐与自定义库；多根目录、扫库、海报墙、继续观看 |
| **元数据刮削** | TMDb、Bangumi、Douban、TheTVDB、Fanart 等；支持 NFO、手动匹配、刮削队列 |
| **播放** | 网页播放器、HLS 转码、弹幕、字幕、播放配置档、观看历史与收藏 |
| **Emby/Jellyfin 客户端兼容** | 内置完整 Emby 服务端协议实现：Infuse、SenPlayer、Fileball、Emby/Jellyfin 官方客户端等可直接把本服务当作 Emby 服务器添加，使用 MeBox 账号登录，海报墙、进度同步、多用户无缝衔接 |
| **远程 Emby 挂载** | 将远程 Emby 媒体库挂载到本地界面统一浏览（无需单独开 Emby 客户端） |
| **网盘与 STRM** | OpenList、CloudDrive2、115、WebDAV 等；STRM 同步、上传/下载队列、直链/302 播放 |
| **下载与整理** | 下载目录定时自动整理（智能分类、自动注册媒体库）、文件管理器（复制/移动/硬链/软链） |
| **用户与权限** | 管理员/普通用户、有效期、成人内容开关、播放配置 PIN、细粒度操作权限 |
| **运维能力** | 统一任务队列、存储统计、DLNA 投屏、系统设置与日志 |

### 技术栈

- **后端**：Go · Gin · GORM · SQLite / PostgreSQL · 可选 Redis · 可选 OpenSearch
- **前端**：React 18 · Vite · TypeScript · Tailwind CSS · Zustand
- **部署**：Docker Compose 多档模板，支持 amd64 / arm64 镜像与单文件可执行发布

---

## 快速开始

推荐使用 Docker Compose。仓库提供四份**互相独立**的完整模板，无需 `.env` 即可起步。

```bash
mkdir -p MeBox && cd MeBox

# 最省心：单镜像 + 内置 SQLite
curl -fsSL https://raw.githubusercontent.com/truewhile/MeBox/main/docker-compose.simple.yml -o docker-compose.yml

# 或多用户场景：PostgreSQL 第一档
# curl -fsSL https://raw.githubusercontent.com/truewhile/MeBox/main/docker-compose.yml -o docker-compose.yml

docker compose up -d
```

浏览器访问：

```text
http://服务器IP:18080
```

默认账号：`admin` / `admin123`（首次登录后请立即修改密码）

> 💡 **Emby 用户无缝切换**：MeBox 完整兼容 Emby/Jellyfin 客户端协议。手机、电视、平板上的 Infuse、SenPlayer、Fileball、Emby/Jellyfin 官方客户端，直接按「添加 Emby 服务器」填入 `http://服务器IP:18080`，用 MeBox 账号登录即可，无需改变原有使用习惯。

镜像地址：

```text
ghcr.io/truewhile/mebox:latest
```

---

## 部署档位

按机器资源选择档位。每份 Compose 文件均可单独使用，**不要**叠加多个 `-f`。

| 档位 | 配置文件 | 组件 | 适合场景 |
| --- | --- | --- | --- |
| 单镜像档 | `docker-compose.simple.yml` | MeBox + SQLite | 新手、单人、低配 NAS，只想一个容器跑起来 |
| 第一档 | `docker-compose.yml` | MeBox + PostgreSQL | 大多数家庭 NAS，多用户更稳 |
| 第二档 | `docker-compose.standard.yml` | + Redis | 多用户、Emby 客户端频繁刷新、首页/列表访问多 |
| 第三档 | `docker-compose.search.yml` | + OpenSearch | 超大媒体库、复杂全文搜索（内存占用更高） |

### 单镜像档要点

- 只启动 **一个** MeBox 容器，数据在 `./data/mebox.db`
- 通常只需改端口与媒体目录挂载
- **不要**设置 `MEBOX_DATABASE_DSN`，否则会切到 PostgreSQL

```yaml
ports:
  - "18080:8080"
volumes:
  - ./data:/data          # 必须备份
  - ./cache:/cache        # 可重建
  - ./media:/media        # 改成你的媒体目录
```

网页添加媒体库时填写容器内路径，例如 `/media`、`/media/电影`。

### PostgreSQL 档位要点

- 主库在 `./postgres`，配置与密钥在 `./data`
- 若存在旧版 `./data/mebox.db`，首次启动会自动迁移到 PostgreSQL
- 迁移完成后可将 `MEBOX_DATABASE_DB_PATH` 改为不存在路径，避免重复检查：

```yaml
MEBOX_DATABASE_DB_PATH: /data/no-sqlite-migration.db
```

### 必须备份与可重建

| 路径 | 说明 |
| --- | --- |
| `./data` | JWT 密钥、运行配置、SQLite 主库或迁移源 |
| `./postgres` | PostgreSQL 主库（PG 档位） |
| `./cache` | 海报/转码缓存，可重建 |
| `./redis` | 热缓存，可重建 |
| `./opensearch` | 搜索索引，可重建 |

### 更新镜像

```bash
docker compose pull mebox
docker compose up -d --no-deps mebox
```

日常更新只拉 `mebox` 服务即可，不要随意 `docker compose pull` 升级 PostgreSQL/Redis/OpenSearch 基础镜像。

---

## 路径映射

Docker 部署最常见的问题是路径填错。记住：

- `volumes` **左侧**是宿主机真实路径，**右侧**是容器内路径
- 网页后台添加媒体库时，应填写**容器内**路径（如 `/media/电影`）
- 若使用自动整理/下载入库，`MEBOX_MEDIA_DIR` 与 `MEBOX_DOWNLOAD_DIR` 需与挂载一致

NAS 示例：

```yaml
volumes:
  - /vol1/1000/Media:/media
  - /vol1/1000/Downloads:/downloads
environment:
  MEBOX_MEDIA_DIR: /vol1/1000/Media
  MEBOX_MEDIA_CONTAINER_DIR: /media
  MEBOX_DOWNLOAD_DIR: /vol1/1000/Downloads
  MEBOX_DOWNLOAD_CONTAINER_DIR: /downloads
```

---

## 首次使用建议

1. **创建媒体库** → 填写 `/media/...` → 执行扫库
2. **配置元数据源** → 系统设置中添加 TMDb、Bangumi 等 API
3. **（可选）配置下载目录自动整理** → 文件管理中将下载目录设为整理源，下载完成后自动分类入库
4. **（可选）配置网盘账号** → STRM 管理中添加 OpenList / 115 / WebDAV 等
5. **第三方播放器** → 以 Emby 服务器添加 `http://服务器IP:18080`，使用 MeBox 账号登录

---

## 常见问题

**扫库或入库很慢？**  
先确认路径映射与数据库档位。网盘扫描还受接口限速与目录规模影响；大库可考虑第二档 Redis 或第三档 OpenSearch。

**下载目录文件没有被自动整理？**  
确认下载目录已通过 `volumes` 挂进容器，且 `MEBOX_DOWNLOAD_*` 环境变量对应正确。MeBox 负责目录整理入库，qBittorrent 等下载器按普通软件自行部署即可。

**硬链接失败（cross-device link）？**  
硬链接要求源与目标在同一文件系统/子卷；跨盘、跨 btrfs 子卷或网盘挂载时请改用复制或软链接。

**日志保留时间太短？**

默认应用日志为 `20MB x 5`，容器 stdout 日志为 `20m x 3`。排障时可在 compose 中调大
`MEBOX_LOGGING_MAX_SIZE_MB`、`MEBOX_LOGGING_MAX_BACKUPS` 与服务的 `logging.options.max-size/max-file`。

**第三方播放器连不上？**  
确认地址为 `http://IP:18080`，使用 MeBox 用户账号；反代部署需正确配置外部 URL 与 HTTPS 头。

---

## 开发构建

后端通过 `go:embed` 嵌入 `web/dist`，**编译前必须先构建前端**。
前端构建要求 Node.js `20.19+` 或 `22.12+`。

```bash
npm --prefix web ci
npm --prefix web run build

go test ./...
go run ./cmd/server          # http://127.0.0.1:8080
npm --prefix web run dev     # http://127.0.0.1:3000
```

CI 会在 Release 中提供 Windows / Linux / macOS 的 amd64、arm64 单文件可执行程序。

---

## 鸣谢

MeBox 在 [MediaStationGo](https://github.com/ShukeBta/MediaStationGo) 的基础上 fork 并持续演进。感谢上游项目在媒体库架构、Emby 协议兼容和自托管体验上的奠基工作。

项目中许多网盘同步、STRM 与媒体整理相关的设计与实现，也参考了 [qmediasync](https://github.com/qicfan/qmediasync)。感谢该项目的思路与实践经验。

---

## 贡献与反馈

提交 Issue 或 Pull Request 前，请阅读 [贡献规范](CONTRIBUTING.md) 与 [安全策略](SECURITY.md)。

- Bug 请附部署方式、复现步骤与相关日志
- 功能建议请说明使用场景与期望行为
- PR 请从独立分支发起，提交前运行 `go test ./...` 与 `npm --prefix web run build`

---

## Star History

<a href="https://www.star-history.com/?repos=truewhile%2FMeBox&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=truewhile/MeBox&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=truewhile/MeBox&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=truewhile/MeBox&type=date&legend=top-left" />
 </picture>
</a>

---

## 许可证

本项目采用 [GPL-3.0](LICENSE) 许可证。

---

## 赞赏

如果 MeBox 帮你把家庭影音折腾明白了，欢迎请作者喝杯咖啡 ☕

<p align="center">
  <img src="docs/images/donation-qr.png" width="320" alt="WhileTrue 的赞赏码" />
</p>

<p align="center">
  <strong>Telegram 交流群</strong>：<a href="https://t.me/MeBoxGroup">https://t.me/MeBoxGroup</a><br/>
  使用问题、功能建议、更新动态，欢迎来群里聊
</p>
