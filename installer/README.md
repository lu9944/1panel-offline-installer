# 1Panel 离线安装包

这个压缩包包含 1Panel、Docker、Docker Compose 以及 MySQL 8.0、Redis、OpenResty 的离线镜像。

## 快速开始

```bash
tar xzf 1panel-offline-*.tar.gz
cd 1panel-offline-*
sudo bash install.sh
```

安装器会：

1. 安装并启动 1Panel；
2. 安装离线 Docker（如果系统还没有 Docker）；
3. 加载 MySQL / Redis / OpenResty 镜像；
4. 把应用注册到 1Panel 应用商店；
5. 自动安装三个应用；
6. 输出面板和数据库凭据，并写入 `/root/1panel-offline-credentials.txt`。

## 完全非交互安装

```bash
sudo bash install.sh -y
```

## 只预置应用，不自动安装

```bash
sudo bash install.sh -y --skip-app-install
```

登录面板后可以在「应用商店」中手动安装 MySQL / Redis / OpenResty。

## 自定义端口和密码

```bash
sudo bash install.sh -y \
  --port 22120 \
  --entrance mypanel \
  --username admin \
  --password 'MyPassword123' \
  --mysql-port 3306 \
  --mysql-password 'MysqlPass123' \
  --redis-port 6379 \
  --redis-password 'RedisPass123' \
  --openresty-http-port 80 \
  --openresty-https-port 443
```

端口被占用时，安装器会自动选择备用端口，并在最终输出中显示实际值。

## 查看帮助

```bash
sudo bash install.sh --help
```

## 验证

```bash
sudo ./bootstrap/1panel-offline-bootstrap verify --base-dir /opt --meta ./meta/apps.json
```

## 系统要求

- Linux x86_64 / aarch64
- root 权限
- systemd（自动安装 Docker 时）
- 建议 8 GB 以上可用磁盘
- 已安装 `iptables` 或 `nft`

安装完成后，如果系统启用了防火墙，请放行 1Panel 面板端口、80/443 以及实际使用的 MySQL/Redis 端口。
