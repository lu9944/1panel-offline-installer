# 1Panel 离线安装包构建器

使用 GitHub Actions 构建 **1Panel Linux 离线安装包**，安装完成后自动完成：

- 安装并启动 **1Panel**
- 离线安装 **Docker + Docker Compose**
- 从官方应用商店预置并自动安装 **MySQL 8.0 / Redis / OpenResty**
- 在 1Panel 的 **应用商店 → 已安装** 中显示三个应用
- 在 1Panel 的 **数据库** 页面中显示 MySQL 和 Redis
- 所有 Docker 镜像已打包在离线包中，目标机器**不需要联网**

## 两种构建方式

仓库包含两个手动触发的 GitHub Actions：

| Action | 用途 |
| --- | --- |
| **Build Latest 1Panel Offline Installer** | 自动解析 1Panel 最新稳定版和最新应用版本；每天定时检查，有更新才构建 |
| **Build Specific 1Panel Offline Version** | 输入指定 1Panel 版本号，也可以指定 MySQL / Redis / OpenResty 版本 |

两个 Action 都支持：

- 构建 `amd64` + `arm64` 两个平台
- 自动执行真实离线安装验收（在 GitHub Runner 上安装 1Panel、启动 MySQL/Redis/OpenResty 并验证状态）
- 创建/更新 GitHub Release，供内网机器直接下载
- `force_rebuild`：即使版本未变化也强制重建

## 最新版工作流

`Build Latest 1Panel Offline Installer` 每天 UTC 02:00 自动运行：

1. 从 `resource.1panel.pro` 解析 1Panel 最新稳定版；
2. 从官方应用商店仓库解析 MySQL 8.0.x、Redis、OpenResty 最新版本；
3. 计算构建指纹（1Panel 版本、应用版本、Docker/Compose 版本、应用商店 commit、本仓库 commit）；
4. 如果对应 Release 已经存在且指纹一致，跳过构建；
5. 否则构建并更新 Release。

也可以随时在 **Actions → Build Latest 1Panel Offline Installer → Run workflow** 手动触发。

## 指定版本工作流

**Actions → Build Specific 1Panel Offline Version → Run workflow**，填写：

| 参数 | 说明 | 默认值 |
| --- | --- | --- |
| `version` | 1Panel 版本，如 `v2.5.1` / `2.5.1` / `latest` | `latest` |
| `mysql_version` | MySQL 版本，留空为最新 `8.0.x` | 空 |
| `redis_version` | Redis 版本，留空为最新 | 空 |
| `openresty_version` | OpenResty 版本，留空为最新 | 空 |
| `docker_version` | Docker static 版本，留空为最新 | 空 |
| `compose_version` | Docker Compose 版本，留空为最新 | 空 |
| `release_tag` | Release 标签，留空为 `offline-<version>` | 空 |
| `create_release` | 创建/更新 Release | `true` |
| `run_acceptance` | 执行真实离线安装验收 | `true` |

例如构建 `v2.5.1` + MySQL `8.0.46` + Redis `8.10.3`：

```text
version            = v2.5.1
mysql_version      = 8.0.46
redis_version      = 8.10.3
openresty_version  = 1.31.8.1-2-2-noble
```

## 下载和安装

从 **Releases** 下载对应架构的压缩包：

```text
1panel-offline-v2.5.1-linux-amd64.tar.gz
1panel-offline-v2.5.1-linux-arm64.tar.gz
```

在目标 Linux 机器上：

```bash
tar xzf 1panel-offline-v2.5.1-linux-amd64.tar.gz
cd 1panel-offline-v2.5.1-linux-amd64

# 交互式安装（推荐）
sudo bash install.sh

# 或完全非交互
sudo bash install.sh -y
```

安装完成后终端会输出面板地址、用户名、密码，以及 MySQL / Redis / OpenResty 的连接信息，同时保存到：

```text
/root/1panel-offline-credentials.txt
```

登录 1Panel 后：

- **应用商店 → 已安装**：看到 MySQL、Redis、OpenResty；
- **数据库 → MySQL**：看到 MySQL 实例；
- **数据库 → Redis**：看到 Redis 实例；
- **网站**：可以直接使用 OpenResty 创建网站。

## 安装器常用参数

```bash
sudo bash install.sh --help

sudo bash install.sh -y \
  --port 22120 \
  --entrance mypanel \
  --username admin \
  --password 'MyPassword123' \
  --mysql-port 3306 \
  --redis-port 6379 \
  --openresty-http-port 80 \
  --openresty-https-port 443
```

| 参数 | 说明 |
| --- | --- |
| `-y, --non-interactive` | 全部使用默认值 |
| `--port` | 1Panel 面板端口，默认随机可用端口 |
| `--entrance` | 安全入口，默认随机 |
| `--username` / `--password` | 面板账号，密码默认随机 |
| `--install-dir` | 1Panel 安装父目录，默认 `/opt`（实际目录 `/opt/1panel`） |
| `--mysql-port` / `--mysql-password` | MySQL 端口/密码 |
| `--redis-port` / `--redis-password` | Redis 端口/密码 |
| `--openresty-http-port` / `--openresty-https-port` | OpenResty 端口 |
| `--skip-app-install` | 只预置应用，不自动安装 |
| `--skip-apps` | 完全跳过应用商店和镜像 |
| `--skip-panel-install` | 只向已有 1Panel 安装/预置应用 |
| `--skip-docker-install` | 不自动安装 Docker（要求已安装） |
| `--no-verify` | 跳过离线包 SHA256 校验 |

> 端口被占用时，安装器会自动选择备用端口（例如 MySQL `13306`、Redis `16379`、OpenResty `8080/8443`），并在最终输出中显示实际端口。

## 离线包内容

```text
1panel-offline-v2.5.1-linux-amd64/
├── install.sh                       # 一键安装器
├── README.md
├── VERSION
├── BUILD_INFO                       # 版本、应用、Docker/Compose、指纹
├── SHA256SUMS
├── 1panel/                          # 1Panel 官方安装包（已离线化补丁）
├── bootstrap/                       # 静态 Go 引导程序（注册应用 + 调用 API 安装）
├── apps/                            # MySQL / Redis / OpenResty 应用定义
├── meta/apps.json                   # 应用元数据
├── docker/                          # Docker 静态二进制 + Compose + service
└── images/                          # 三个应用的 Docker 镜像 tar.gz
```

## 实现方式

### 1. 1Panel 离线化

- 从 1Panel 官方发布地址下载 Linux 安装包，并用官方 `checksums.txt` 做 SHA256 校验；
- 构建时修补上游 `install.sh` 中的 `Get_Ip()` 在线探测（`api64.ipify.org`），避免离线安装时卡住；
- 构建时把 `1panel-agent` / `1panel-core` 内置配置里的 `is_offline: false` 改为离线模式。
  否则 1Panel 每次启动都会尝试同步远程应用商店，失败后把 `AppStoreLastModified` 重置为 0，
  导致预置的离线应用无法安装；
- 使用内置的 Docker 静态二进制安装 Docker，不调用在线安装脚本。

### 2. 应用商店离线化

1Panel 社区版默认从远程同步应用商店。安装器在面板停止时：

- 将官方应用商店中的 MySQL 8.0 / Redis / OpenResty 定义复制到
  `/opt/1panel/resource/apps/remote/`；
- 写入 `apps`、`app_details`、`app_tags` 和 `tags` 表；
- 写入 `.1panel-appstore-version` 缓存标记，避免面板再次联网下载；
- 将 `AppStoreSyncStatus` 设置为成功。

因此面板启动后，应用商店中直接可见这些应用，且资源路径与官方远程应用一致。

### 3. 自动安装应用

安装器使用 1Panel 官方 API：

- `POST /api/v2/apps/install`
- `GET /api/v2/apps/installed/info/{id}`

自动登录需要与 1Panel 前端一致的 RSA + AES 密码加密，由包内的静态 Go 程序完成，不依赖 Python、curl 或 jq。

安装 MySQL/Redis 时，1Panel 会同时创建 `databases` 记录，所以它们会出现在「数据库」页面。

### 4. 镜像离线加载

构建时使用 `skopeo` 从 Docker Hub 导出指定架构的镜像：

- `mysql:<8.0.x>`
- `redis:<latest>`
- `1panel/openresty:<latest>`

安装时执行 `docker load`，然后以 `pullImage=false` 安装应用。

## 环境要求

目标机器：

- Linux x86_64 或 aarch64
- root 权限
- systemd（自动安装 Docker 时需要）
- 建议至少 8 GB 可用磁盘空间（镜像解压后约 4-5 GB）
- 已安装 `iptables` 或 `nft`（绝大多数服务器默认已有）

如果目标机器已经安装并运行 Docker，安装器会复用，不会覆盖 Docker 配置。

## 安全与校验

- 1Panel 官方安装包使用官方 `checksums.txt` 校验；
- Docker static 包和 Docker Compose 使用官方 `.sha256` 校验；
- 应用来自官方 `1Panel-dev/appstore` 仓库，构建时记录 commit；
- Release 提供两个架构的 `*.tar.gz.sha256` 和 `SHA256SUMS`；
- 离线包内 `SHA256SUMS` 覆盖所有文件，`install.sh` 安装前自动校验；
- CI 会在 GitHub Runner 上执行真实离线安装，验证三个应用状态为 `Running` 后才发布 Release。

## 本地构建

依赖：Python 3.10+、PyYAML、Go 1.24+、Docker（或 skopeo）、git、curl、tar、gzip。

```bash
python3 -m pip install -r scripts/requirements-build.txt

python3 scripts/build-offline-package.py \
  --version latest \
  --arch amd64,arm64 \
  --out-dir dist
```

指定版本：

```bash
python3 scripts/build-offline-package.py \
  --version v2.5.1 \
  --mysql-version 8.0.46 \
  --redis-version 8.10.3 \
  --openresty-version 1.31.8.1-2-2-noble \
  --arch amd64
```

只构建元数据（不下载镜像，用于调试）：

```bash
python3 scripts/build-offline-package.py --version latest --arch amd64 --skip-images
```

## 常见问题

### 安装后应用商店看不到应用

确认安装器没有被 `--skip-apps` 跳过，并检查：

```bash
sudo 1pctl status
sudo ls /opt/1panel/resource/apps/remote/mysql
sudo ./bootstrap/1panel-offline-bootstrap verify --base-dir /opt --meta ./meta/apps.json
```

### Docker 启动失败

```bash
sudo systemctl status docker
sudo journalctl -u docker -n 100
```

常见原因：缺少 `iptables`/`nft`、内核不支持 overlay2、已有冲突的 Docker 配置。

### OpenResty 80/443 被占用

安装器会自动改用 `8080/8443`。也可以显式指定：

```bash
sudo bash install.sh --openresty-http-port 8088 --openresty-https-port 8443
```

### 目标机器没有 systemd

可以手动安装 Docker 后使用：

```bash
sudo bash install.sh -y --skip-docker-install
```

## 目录结构

```text
.github/workflows/          GitHub Actions
cmd/bootstrap/              静态 Go 引导程序
installer/                  目标机器安装脚本和 Docker 配置
scripts/                    构建、版本解析、指纹检查
tests/                      单元测试/校验脚本
```

## License

MIT
