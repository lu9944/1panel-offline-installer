#!/usr/bin/env bash
#
# 1Panel 离线安装器（Linux）
#
# 功能:
#   * 完全离线安装 1Panel（不访问互联网）
#   * 可选自动安装离线 Docker（内置 Docker 静态二进制）
#   * 预置应用商店中的 MySQL 8.0 / Redis / OpenResty 并自动安装
#   * 安装完成后可在 1Panel「应用商店 → 已安装」以及「数据库」中看到
#
# 用法: sudo bash install.sh [选项]
#
set -euo pipefail

BUNDLE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_INFO="$BUNDLE_DIR/BUILD_INFO"

RED=$'\033[0;31m'
GREEN=$'\033[0;32m'
YELLOW=$'\033[0;33m'
BLUE=$'\033[0;34m'
MUTED=$'\033[0;2m'
NC=$'\033[0m'

die()  { printf '%s\n' "${RED}错误: $*${NC}" >&2; exit 1; }
warn() { printf '%s\n' "${YELLOW}警告: $*${NC}" >&2; }
log()  { printf '%s\n' "${BLUE}==>${NC} $*"; }
ok()   { printf '%s\n' "${GREEN}==>${NC} $*"; }
gray() { printf '%s\n' "${MUTED}$*${NC}"; }

usage() {
    cat <<'EOF'
1Panel 离线安装器（Linux）

用法:
  sudo bash install.sh [选项]

常用选项:
  -y, --non-interactive          使用默认值，不进行交互提问
      --port <端口>              1Panel 面板端口（默认自动选择）
      --entrance <安全入口>      1Panel 安全入口（默认随机生成）
      --username <用户名>        1Panel 用户名（默认 admin）
      --password <密码>          1Panel 密码（默认随机生成）
      --install-dir <目录>       1Panel 安装父目录（默认 /opt，最终安装到 /opt/1panel）
      --panel-url <URL>          已安装面板的访问地址（配合 --skip-panel-install）

应用相关:
      --mysql-port <端口>        MySQL 端口（默认 3306，被占用时自动换端口）
      --mysql-password <密码>    MySQL root 密码（默认随机生成）
      --redis-port <端口>        Redis 端口（默认 6379，被占用时自动换端口）
      --redis-password <密码>    Redis 密码（默认随机生成）
      --openresty-http-port <端口>   OpenResty HTTP 端口（默认 80）
      --openresty-https-port <端口>  OpenResty HTTPS 端口（默认 443）
      --skip-app-install         只预置应用到应用商店，不自动安装
      --skip-apps                完全跳过应用商店预置、镜像加载和自动安装
      --skip-images              不加载 Docker 镜像（仅调试用）
      --skip-docker-install      不安装 Docker（要求系统已安装并运行 Docker）
      --skip-panel-install       不安装 1Panel，仅向已有面板预置/安装应用

其他:
      --no-verify                跳过离线包 SHA256 校验
      --list                     列出离线包版本、平台和应用
  -h, --help                     显示本帮助

注意:
  * 自动安装需要 root 权限和 systemd。
  * 目标机器建议至少 8 GB 可用磁盘空间。
  * 安装完成后凭据保存在 /root/1panel-offline-credentials.txt。
EOF
}

need_build_info() {
    [[ -f "$BUILD_INFO" ]] || die "缺少 BUILD_INFO，离线包不完整"
}

info() {
    sed -n "s/^$1=//p" "$BUILD_INFO" | head -1
}

app_version() {
    local key="$1"
    local line
    line="$(info apps)"
    tr ',' '\n' <<<"$line" | awk -F: -v k="$key" '$1==k {print $2; exit}'
}

app_present() {
    [[ -n "$(app_version "$1")" ]]
}

random_hex() {
    local n="${1:-16}"
    od -An -N"$n" -tx1 /dev/urandom | tr -d ' \n'
}

random_password() {
    random_hex 12
}

random_port() {
    od -An -N2 -tu2 /dev/urandom | awk '{print 20000 + ($1 % 20000)}'
}

is_root() {
    [[ "$(id -u)" -eq 0 ]]
}

port_in_use() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -H -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}$" && return 0
        ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}$" && return 0
    elif command -v netstat >/dev/null 2>&1; then
        netstat -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]${port}$" && return 0
    fi
    return 1
}

choose_port() {
    # choose_port <explicit> <default> <fallback> <label>
    local explicit="$1" default="$2" fallback="$3" label="$4"
    if [[ -n "$explicit" ]]; then
        [[ "$explicit" =~ ^[0-9]+$ ]] || die "$label 端口不是数字: $explicit"
        port_in_use "$explicit" && die "$label 端口 $explicit 已被占用，请更换端口"
        printf '%s' "$explicit"
        return
    fi
    if ! port_in_use "$default"; then
        printf '%s' "$default"
        return
    fi
    warn "$label 默认端口 $default 已被占用，尝试 $fallback"
    if ! port_in_use "$fallback"; then
        printf '%s' "$fallback"
        return
    fi
    local p
    for _ in $(seq 1 100); do
        p="$(random_port)"
        if ! port_in_use "$p"; then
            printf '%s' "$p"
            return
        fi
    done
    die "$label 找不到可用端口"
}

panel_service_cmd() {
    local action="$1"
    if command -v systemctl >/dev/null 2>&1; then
        systemctl "$action" 1panel-agent 1panel-core >/dev/null 2>&1 || true
    elif command -v rc-service >/dev/null 2>&1; then
        rc-service 1panel-agent "$action" >/dev/null 2>&1 || true
        rc-service 1panel-core "$action" >/dev/null 2>&1 || true
    else
        /etc/init.d/1panel-agent "$action" >/dev/null 2>&1 || true
        /etc/init.d/1panel-core "$action" >/dev/null 2>&1 || true
    fi
}

panel_start() {
    if command -v systemctl >/dev/null 2>&1; then
        systemctl start 1panel-agent >/dev/null 2>&1 || true
        sleep 1
        systemctl start 1panel-core >/dev/null 2>&1 || true
    elif command -v rc-service >/dev/null 2>&1; then
        rc-service 1panel-agent start >/dev/null 2>&1 || true
        rc-service 1panel-core start >/dev/null 2>&1 || true
    else
        /etc/init.d/1panel-agent start >/dev/null 2>&1 || true
        /etc/init.d/1panel-core start >/dev/null 2>&1 || true
    fi
}

docker_ready() {
    command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1
}

wait_docker() {
    local i
    for i in $(seq 1 60); do
        if docker_ready; then
            return 0
        fi
        sleep 2
    done
    return 1
}

install_compose_binary() {
    local compose="$BUNDLE_DIR/docker/compose/docker-compose"
    [[ -x "$compose" ]] || return 0
    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        return 0
    fi
    if command -v docker-compose >/dev/null 2>&1 && docker-compose version >/dev/null 2>&1; then
        return 0
    fi
    install -m 0755 "$compose" /usr/local/bin/docker-compose
    mkdir -p /usr/local/lib/docker/cli-plugins /usr/libexec/docker/cli-plugins
    ln -sf /usr/local/bin/docker-compose /usr/local/lib/docker/cli-plugins/docker-compose
    ln -sf /usr/local/bin/docker-compose /usr/libexec/docker/cli-plugins/docker-compose
}

install_docker_offline() {
    local docker_payload="$BUNDLE_DIR/docker"
    [[ -d "$docker_payload/bin" && -x "$docker_payload/bin/dockerd" ]] || die "离线包未包含 Docker 静态二进制"
    command -v systemctl >/dev/null 2>&1 || die "离线 Docker 安装需要 systemd"
    command -v iptables >/dev/null 2>&1 || command -v nft >/dev/null 2>&1 || \
        die "缺少 iptables/nft，无法启动 Docker；请先在目标机器安装 iptables"

    log "安装离线 Docker ..."
    install -m 0755 "$docker_payload"/bin/* /usr/bin/
    install -m 0644 "$docker_payload/service/docker.service" /etc/systemd/system/docker.service
    mkdir -p /etc/docker
    if [[ ! -f /etc/docker/daemon.json ]]; then
        install -m 0644 "$docker_payload/conf/daemon.json" /etc/docker/daemon.json
    else
        warn "已存在 /etc/docker/daemon.json，保留原配置"
    fi
    install_compose_binary
    systemctl daemon-reload
    systemctl enable docker >/dev/null 2>&1 || true
    systemctl restart docker
    if ! wait_docker; then
        journalctl -u docker -n 40 --no-pager 2>/dev/null || true
        die "Docker 启动失败，请检查上方日志"
    fi
    ok "Docker 已启动: $(docker --version)"
}

ensure_compose() {
    install_compose_binary
    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        return 0
    fi
    if command -v docker-compose >/dev/null 2>&1 && docker-compose version >/dev/null 2>&1; then
        return 0
    fi
    die "未找到 Docker Compose；离线包应包含 docker/compose/docker-compose"
}

load_images() {
    local manifest="$BUNDLE_DIR/images/manifest.txt"
    [[ -f "$manifest" ]] || die "缺少 images/manifest.txt"
    local key image file
    while IFS=$'\t' read -r key image file; do
        [[ -z "${key:-}" || "$key" == \#* ]] && continue
        [[ -n "$file" ]] || continue
        if docker image inspect "$image" >/dev/null 2>&1; then
            gray "镜像已存在，跳过: $image"
            continue
        fi
        log "加载镜像 $image ..."
        docker load -i "$BUNDLE_DIR/images/$file" || die "镜像加载失败: $image"
        docker image inspect "$image" >/dev/null 2>&1 || die "镜像加载后校验失败: $image"
    done < "$manifest"
    ok "Docker 镜像加载完成"
}

verify_checksums() {
    local sums="$BUNDLE_DIR/SHA256SUMS"
    [[ -f "$sums" ]] || die "缺少 SHA256SUMS"
    log "校验离线包完整性 ..."
    if command -v sha256sum >/dev/null 2>&1; then
        (cd "$BUNDLE_DIR" && sha256sum -c --status SHA256SUMS) || die "SHA256 校验失败，离线包可能损坏"
    elif command -v shasum >/dev/null 2>&1; then
        (cd "$BUNDLE_DIR" && shasum -a 256 -c --status SHA256SUMS) || die "SHA256 校验失败，离线包可能损坏"
    else
        warn "系统缺少 sha256sum/shasum，跳过校验"
    fi
    ok "完整性校验通过"
}

read_1pctl_var() {
    local name="$1"
    [[ -f /usr/local/bin/1pctl ]] || return 0
    sed -n "s/^${name}=//p" /usr/local/bin/1pctl | head -1
}

print_credentials() {
    local ip
    ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [[ -n "$ip" ]] || ip="127.0.0.1"
    printf '1Panel 地址: http://%s:%s/%s\n' "$ip" "$PANEL_PORT" "$PANEL_ENTRANCE"
    printf '1Panel 用户名: %s\n' "$PANEL_USERNAME"
    printf '1Panel 密码: %s\n' "$PANEL_PASSWORD"
    if [[ "$SKIP_APPS" != true ]]; then
        if app_present mysql; then
            printf 'MySQL 地址: %s:%s  root 密码: %s\n' "$ip" "$MYSQL_PORT" "$MYSQL_PASSWORD"
        fi
        if app_present redis; then
            printf 'Redis 地址: %s:%s  密码: %s\n' "$ip" "$REDIS_PORT" "$REDIS_PASSWORD"
        fi
        if app_present openresty; then
            printf 'OpenResty HTTP/HTTPS: %s:%s / %s:%s\n' "$ip" "$OPENRESTY_HTTP_PORT" "$ip" "$OPENRESTY_HTTPS_PORT"
        fi
    fi
}

write_credentials_file() {
    local file="/root/1panel-offline-credentials.txt"
    umask 077
    {
        echo "# 1Panel 离线安装凭据 $(date '+%Y-%m-%d %H:%M:%S')"
        print_credentials
    } > "$file"
    chmod 600 "$file"
    echo "凭据已保存到: $file"
}

# ---------------- 参数解析 ----------------
NON_INTERACTIVE=false
PANEL_PORT=""
PANEL_ENTRANCE=""
PANEL_USERNAME="admin"
PANEL_PASSWORD=""
PANEL_INSTALL_DIR="/opt"
PANEL_URL=""
MYSQL_PORT=""
MYSQL_PASSWORD=""
REDIS_PORT=""
REDIS_PASSWORD=""
OPENRESTY_HTTP_PORT=""
OPENRESTY_HTTPS_PORT=""
SKIP_PANEL_INSTALL=false
SKIP_APP_INSTALL=false
SKIP_APPS=false
SKIP_IMAGES=false
SKIP_DOCKER_INSTALL=false
SKIP_VERIFY=false
LIST_ONLY=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        -y|--non-interactive) NON_INTERACTIVE=true; shift ;;
        --port) [[ -n "${2:-}" ]] || die "--port 需要一个参数"; PANEL_PORT="$2"; shift 2 ;;
        --entrance) [[ -n "${2:-}" ]] || die "--entrance 需要一个参数"; PANEL_ENTRANCE="$2"; shift 2 ;;
        --username) [[ -n "${2:-}" ]] || die "--username 需要一个参数"; PANEL_USERNAME="$2"; shift 2 ;;
        --password) [[ -n "${2:-}" ]] || die "--password 需要一个参数"; PANEL_PASSWORD="$2"; shift 2 ;;
        --install-dir) [[ -n "${2:-}" ]] || die "--install-dir 需要一个参数"; PANEL_INSTALL_DIR="$2"; shift 2 ;;
        --panel-url) [[ -n "${2:-}" ]] || die "--panel-url 需要一个参数"; PANEL_URL="$2"; shift 2 ;;
        --mysql-port) [[ -n "${2:-}" ]] || die "--mysql-port 需要一个参数"; MYSQL_PORT="$2"; shift 2 ;;
        --mysql-password) [[ -n "${2:-}" ]] || die "--mysql-password 需要一个参数"; MYSQL_PASSWORD="$2"; shift 2 ;;
        --redis-port) [[ -n "${2:-}" ]] || die "--redis-port 需要一个参数"; REDIS_PORT="$2"; shift 2 ;;
        --redis-password) [[ -n "${2:-}" ]] || die "--redis-password 需要一个参数"; REDIS_PASSWORD="$2"; shift 2 ;;
        --openresty-http-port) [[ -n "${2:-}" ]] || die "--openresty-http-port 需要一个参数"; OPENRESTY_HTTP_PORT="$2"; shift 2 ;;
        --openresty-https-port) [[ -n "${2:-}" ]] || die "--openresty-https-port 需要一个参数"; OPENRESTY_HTTPS_PORT="$2"; shift 2 ;;
        --skip-panel-install) SKIP_PANEL_INSTALL=true; shift ;;
        --skip-app-install) SKIP_APP_INSTALL=true; shift ;;
        --skip-apps) SKIP_APPS=true; shift ;;
        --skip-images) SKIP_IMAGES=true; shift ;;
        --skip-docker-install) SKIP_DOCKER_INSTALL=true; shift ;;
        --no-verify) SKIP_VERIFY=true; shift ;;
        --list) LIST_ONLY=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) die "未知参数: $1（用 --help 查看用法）" ;;
    esac
done

need_build_info
VERSION="$(info version)"
ARCH="$(info arch)"
APPS_LINE="$(info apps)"
STORE_VERSION="$(info appstore_version)"
DOCKER_VERSION="$(info docker_version)"
COMPOSE_VERSION="$(info compose_version)"

if [[ "$LIST_ONLY" == true ]]; then
    echo "1Panel 版本: $VERSION"
    echo "平台:       linux-$ARCH"
    echo "Docker:     $DOCKER_VERSION"
    echo "Compose:    $COMPOSE_VERSION"
    echo "应用:       $APPS_LINE"
    exit 0
fi

is_root || die "请使用 root 运行（sudo bash install.sh）"
[[ "$(uname -s)" == "Linux" ]] || die "此离线包仅支持 Linux"

case "$ARCH" in
    amd64)  want_arch="x86_64" ;;
    arm64)  want_arch="aarch64" ;;
    *)      die "未知的离线包架构: $ARCH" ;;
esac
current_arch="$(uname -m)"
if [[ "$current_arch" != "$want_arch" ]]; then
    die "离线包架构为 $ARCH（$want_arch），当前机器为 $current_arch"
fi

if [[ "$SKIP_VERIFY" != true ]]; then
    verify_checksums
fi

[[ -x "$BUNDLE_DIR/bootstrap/1panel-offline-bootstrap" ]] || die "缺少 bootstrap 引导程序"
[[ -d "$BUNDLE_DIR/1panel" ]] || die "缺少 1panel 安装文件"

free_kb="$(df -Pk "$PANEL_INSTALL_DIR" 2>/dev/null | awk 'NR==2 {print $4}')"
[[ -n "$free_kb" ]] || free_kb="$(df -Pk / | awk 'NR==2 {print $4}')"
if [[ -n "$free_kb" && "$free_kb" -lt 2097152 ]]; then
    warn "$PANEL_INSTALL_DIR 可用空间不足 2 GB，安装可能失败"
elif [[ -n "$free_kb" && "$free_kb" -lt 8388608 ]]; then
    warn "$PANEL_INSTALL_DIR 可用空间少于 8 GB，加载镜像和初始化 MySQL 可能空间不足"
fi

# 读取已有面板信息
if [[ "$SKIP_PANEL_INSTALL" == true ]]; then
    [[ -n "$PANEL_PORT" ]] || PANEL_PORT="$(read_1pctl_var ORIGINAL_PORT)"
    [[ -n "$PANEL_ENTRANCE" ]] || PANEL_ENTRANCE="$(read_1pctl_var ORIGINAL_ENTRANCE)"
    [[ -n "$PANEL_USERNAME" ]] || PANEL_USERNAME="$(read_1pctl_var ORIGINAL_USERNAME)"
    if [[ -z "$(read_1pctl_var BASE_DIR)" ]]; then
        die "未找到 /usr/local/bin/1pctl，--skip-panel-install 仅用于已安装 1Panel 的机器"
    fi
    PANEL_INSTALL_DIR="$(read_1pctl_var BASE_DIR)"
    [[ -n "$PANEL_PORT" ]] || PANEL_PORT=22120
    [[ -n "$PANEL_URL" ]] || PANEL_URL="http://127.0.0.1:${PANEL_PORT}"
    if [[ -z "$PANEL_PASSWORD" ]]; then
        if [[ -t 0 && "$NON_INTERACTIVE" != true ]]; then
            read -r -s -p "请输入现有 1Panel 用户密码: " PANEL_PASSWORD
            echo
        fi
        [[ -n "$PANEL_PASSWORD" ]] || die "使用 --skip-panel-install 时必须通过 --password 提供面板密码"
    fi
else
    [[ -n "$PANEL_PASSWORD" ]] || PANEL_PASSWORD="$(random_password)"
    [[ -n "$PANEL_PORT" ]] || PANEL_PORT="$(random_port)"
fi

[[ -n "$PANEL_ENTRANCE" ]] || PANEL_ENTRANCE="$(random_hex 5)"
[[ -n "$PANEL_USERNAME" ]] || PANEL_USERNAME="admin"

if [[ "$NON_INTERACTIVE" != true && -t 0 && "$SKIP_PANEL_INSTALL" != true ]]; then
    printf '%s\n' "${GREEN}1Panel 离线安装配置${NC}"
    read -r -p "面板端口 [$PANEL_PORT]: " input; [[ -n "$input" ]] && PANEL_PORT="$input"
    read -r -p "安全入口 [$PANEL_ENTRANCE]: " input; [[ -n "$input" ]] && PANEL_ENTRANCE="$input"
    read -r -p "用户名 [$PANEL_USERNAME]: " input; [[ -n "$input" ]] && PANEL_USERNAME="$input"
    read -r -s -p "密码 [自动生成，直接回车]: " input; echo
    [[ -n "$input" ]] && PANEL_PASSWORD="$input"
    if [[ "$SKIP_APPS" != true ]]; then
        read -r -p "自动安装 MySQL 8.0 / Redis / OpenResty？[Y/n]: " input
        case "$input" in
            [nN]*) SKIP_APP_INSTALL=true ;;
        esac
    fi
fi

[[ "$PANEL_PORT" =~ ^[0-9]+$ ]] || die "面板端口不是数字: $PANEL_PORT"
if [[ "$SKIP_PANEL_INSTALL" != true ]]; then
    port_in_use "$PANEL_PORT" && die "面板端口 $PANEL_PORT 已被占用"
fi
[[ "$PANEL_ENTRANCE" =~ ^[a-zA-Z0-9_]{3,30}$ ]] || die "安全入口只能包含字母、数字、下划线，长度 3-30"
[[ "$PANEL_USERNAME" =~ ^[a-zA-Z0-9_]{3,30}$ ]] || die "用户名只能包含字母、数字、下划线，长度 3-30"
[[ "$PANEL_PASSWORD" =~ ^[a-zA-Z0-9_!@#$%*,.?]{8,30}$ ]] || die "密码长度需为 8-30，且只能包含字母、数字和 _!@#$%*,.?"

if [[ "$SKIP_APPS" != true ]]; then
    if app_present mysql; then
        MYSQL_PORT="$(choose_port "$MYSQL_PORT" 3306 13306 "MySQL")"
        [[ -n "$MYSQL_PASSWORD" ]] || MYSQL_PASSWORD="$(random_password)"
    fi
    if app_present redis; then
        REDIS_PORT="$(choose_port "$REDIS_PORT" 6379 16379 "Redis")"
        [[ -n "$REDIS_PASSWORD" ]] || REDIS_PASSWORD="$(random_password)"
    fi
    if app_present openresty; then
        OPENRESTY_HTTP_PORT="$(choose_port "$OPENRESTY_HTTP_PORT" 80 8080 "OpenResty HTTP")"
        OPENRESTY_HTTPS_PORT="$(choose_port "$OPENRESTY_HTTPS_PORT" 443 8443 "OpenResty HTTPS")"
    fi
fi

if [[ "$SKIP_IMAGES" == true && "$SKIP_APPS" != true && "$SKIP_APP_INSTALL" != true ]]; then
    die "--skip-images 只能与 --skip-app-install 或 --skip-apps 一起使用"
fi

# ---------------- 安装 Docker ----------------
if [[ "$SKIP_APPS" != true ]]; then
    if docker_ready; then
        gray "检测到 Docker: $(docker --version)"
    elif command -v docker >/dev/null 2>&1; then
        log "检测到 Docker 已安装但未运行，尝试启动 ..."
        if command -v systemctl >/dev/null 2>&1; then
            systemctl start docker >/dev/null 2>&1 || true
        elif command -v service >/dev/null 2>&1; then
            service docker start >/dev/null 2>&1 || true
        fi
        if ! wait_docker; then
            journalctl -u docker -n 40 --no-pager 2>/dev/null || true
            die "Docker 已安装但无法启动，请先修复 Docker 后重试"
        fi
        gray "Docker 已启动: $(docker --version)"
    else
        if [[ "$SKIP_DOCKER_INSTALL" == true ]]; then
            die "Docker 未安装，且指定了 --skip-docker-install"
        fi
        install_docker_offline
    fi
    ensure_compose
fi

# ---------------- 安装 1Panel ----------------
if [[ "$SKIP_PANEL_INSTALL" != true ]]; then
    if command -v 1panel >/dev/null 2>&1 || [[ -f /usr/local/bin/1pctl ]]; then
        die "检测到已安装 1Panel；升级/复用请使用 --skip-panel-install"
    fi
    if [[ -d "$PANEL_INSTALL_DIR/1panel" ]]; then
        die "$PANEL_INSTALL_DIR/1panel 已存在，请先备份或使用 --install-dir 指定其他目录"
    fi

    RUN_PANEL_DIR="$BUNDLE_DIR/1panel"
    RUN_CLEANUP_DIR=""
    if [[ ! -w "$BUNDLE_DIR/1panel" ]]; then
        log "离线包目录不可写，复制安装文件到临时目录 ..."
        RUN_CLEANUP_DIR="$(mktemp -d)"
        cp -a "$BUNDLE_DIR/1panel" "$RUN_CLEANUP_DIR/1panel"
        RUN_PANEL_DIR="$RUN_CLEANUP_DIR/1panel"
        trap 'rm -rf "$RUN_CLEANUP_DIR"' EXIT
    fi

    log "安装 1Panel $VERSION 到 $PANEL_INSTALL_DIR ..."
    (
        cd "$RUN_PANEL_DIR"
        bash install.sh \
            --non-interactive \
            --lang zh \
            --install-dir "$PANEL_INSTALL_DIR" \
            --port "$PANEL_PORT" \
            --entrance "$PANEL_ENTRANCE" \
            --username "$PANEL_USERNAME" \
            --password "$PANEL_PASSWORD" \
            --install-docker n \
            --configure-accelerator n \
            --replace-daemon-json n
    )
    ok "1Panel 安装完成"
    BASE_DIR="$(read_1pctl_var BASE_DIR)"
    [[ -n "$BASE_DIR" ]] || die "无法从 /usr/local/bin/1pctl 读取 BASE_DIR"
else
    BASE_DIR="$PANEL_INSTALL_DIR"
    log "使用已有 1Panel 安装: $BASE_DIR"
fi

if [[ "$SKIP_APPS" == true ]]; then
    log "已跳过应用商店预置和自动安装"
    echo
    print_credentials
    write_credentials_file
    exit 0
fi

log "等待 1Panel Agent 完成初始化 ..."
"$BUNDLE_DIR/bootstrap/1panel-offline-bootstrap" wait --base-dir "$BASE_DIR" --timeout 3m

# ---------------- 停止面板，写入离线应用商店 ----------------
log "停止 1Panel 服务以写入离线应用商店 ..."
panel_service_cmd stop
sleep 2

log "确认 1Panel 离线模式 ..."
"$BUNDLE_DIR/bootstrap/1panel-offline-bootstrap" patch --binary-dir /usr/local/bin

if [[ "$SKIP_IMAGES" != true ]]; then
    load_images
else
    warn "已跳过 Docker 镜像加载；自动安装应用可能会失败"
fi

log "向 1Panel 注册离线应用商店 ..."
"$BUNDLE_DIR/bootstrap/1panel-offline-bootstrap" seed \
    --base-dir "$BASE_DIR" \
    --apps-dir "$BUNDLE_DIR/apps" \
    --meta "$BUNDLE_DIR/meta/apps.json" \
    --store-version "$STORE_VERSION"

log "启动 1Panel 服务 ..."
panel_start

if [[ "$SKIP_APP_INSTALL" == true ]]; then
    ok "应用已预置到应用商店，未自动安装（--skip-app-install）"
    echo "登录面板后可在「应用商店」中手动安装 MySQL / Redis / OpenResty。"
    echo
    print_credentials
    write_credentials_file
    exit 0
fi

# ---------------- 通过面板 API 自动安装应用 ----------------
log "通过 1Panel API 安装应用（MySQL / Redis / OpenResty）..."
install_args=(
    install
    --base-dir "$BASE_DIR"
    --meta "$BUNDLE_DIR/meta/apps.json"
    --panel-url "${PANEL_URL:-http://127.0.0.1:$PANEL_PORT}"
    --entrance "$PANEL_ENTRANCE"
    --username "$PANEL_USERNAME"
    --password "$PANEL_PASSWORD"
    --timeout 20m
)
if [[ -n "$MYSQL_PASSWORD" ]]; then install_args+=(--mysql-password "$MYSQL_PASSWORD"); fi
if [[ -n "$REDIS_PASSWORD" ]]; then install_args+=(--redis-password "$REDIS_PASSWORD"); fi
if [[ -n "$MYSQL_PORT" ]]; then install_args+=(--mysql-port "$MYSQL_PORT"); fi
if [[ -n "$REDIS_PORT" ]]; then install_args+=(--redis-port "$REDIS_PORT"); fi
if [[ -n "$OPENRESTY_HTTP_PORT" ]]; then install_args+=(--openresty-http-port "$OPENRESTY_HTTP_PORT"); fi
if [[ -n "$OPENRESTY_HTTPS_PORT" ]]; then install_args+=(--openresty-https-port "$OPENRESTY_HTTPS_PORT"); fi
"$BUNDLE_DIR/bootstrap/1panel-offline-bootstrap" "${install_args[@]}"

echo
ok "1Panel $VERSION 离线安装完成"
echo
print_credentials
write_credentials_file
echo
echo "提示: 首次登录后可在「应用商店 → 已安装」看到 MySQL / Redis / OpenResty，"
echo "      MySQL 和 Redis 同时会出现在「数据库」页面。"
echo "      如目标机器启用了防火墙，请放行面板端口以及 80/443、${MYSQL_PORT}、${REDIS_PORT}。"
