#!/usr/bin/env bash
# Ubuntu 22.04 / 24.04；从公开仓库的固定提交构建，不携带 Git 历史。
set +x
set +v
set -Eeuo pipefail
umask 077

readonly REPOSITORY='JnmHub/tokenav-gateway-lite'
readonly PROJECT='tokenav-gateway-lite'
install_dir='/opt/tokenav-gateway-lite'
requested_port='8080'
port_supplied=0
scratch=''
run_log=''
source_dir=''
new_admin_password=''

say() { printf '%s\n' "$*"; }
die() { printf '错误：%s\n' "$*" >&2; exit 1; }
usage() {
    printf '%s\n' \
        'TokenAV 轻量网关安装 / 更新（Ubuntu 22.04、24.04）' \
        '用法：sudo bash install.sh [--dir /opt/tokenav-gateway-lite] [--port 8080]' \
        '  --dir   安装目录，必须为 root 管理的绝对路径；更新时沿用原目录。' \
        '  --port  首次安装的宿主端口（1—65535）；已有 .env 不会被修改。' \
        '  --help  显示帮助，不执行安装。' \
        '请先下载脚本到文件，再用 bash 执行；不支持从标准输入执行。'
}
valid_port() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && (( 10#$1 >= 1 && 10#$1 <= 65535 )); }

while (( $# )); do
    case "$1" in
        --dir) (( $# >= 2 )) || die '--dir 缺少目录。'; install_dir=$2; shift 2 ;;
        --port) (( $# >= 2 )) || die '--port 缺少端口。'; requested_port=$2; port_supplied=1; shift 2 ;;
        --help) usage; exit 0 ;;
        *) die '未知参数；请使用 --help 查看用法。' ;;
    esac
done
valid_port "$requested_port" || die '端口必须为 1—65535 的整数。'
requested_port=$((10#$requested_port))
# 只接受简单路径，避免把共享目录、路径跳转或 Compose 插值字符当作安装根目录。
[[ "$install_dir" =~ ^/([A-Za-z0-9_-][A-Za-z0-9_.-]*/)+[A-Za-z0-9_-][A-Za-z0-9_.-]*$ ]] || die '--dir 必须为至少两级、无空格的规范绝对路径，例如 /srv/tokenav-gateway-lite。'
case "$install_dir/" in
    /dev/*|/proc/*|/sys/*|/run/*|/tmp/*|/var/tmp/*|/etc/*|/usr/*|/bin/*|/sbin/*|/boot/*)
        die '请使用独立数据目录，例如 /opt/tokenav-gateway-lite 或 /srv/tokenav-gateway-lite。' ;;
esac
[[ -f "${BASH_SOURCE[0]:-}" ]] || die '请先下载脚本到文件，再执行 bash 文件名。'
if (( EUID != 0 )); then
    command -v sudo >/dev/null || die '请使用 root 执行，或先安装并配置 sudo。'
    args=(--dir "$install_dir")
    (( port_supplied == 0 )) || args+=(--port "$requested_port")
    exec sudo -- /bin/bash "$(realpath -- "${BASH_SOURCE[0]}")" "${args[@]}"
fi
export PATH='/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
[[ -r /etc/os-release ]] || die '无法识别操作系统。'
# 仅读取系统提供的发行版信息；用户的 .env 从不作为 shell 脚本执行。
. /etc/os-release
[[ "${ID:-}" == ubuntu && ( "${VERSION_ID:-}" == 22.04 || "${VERSION_ID:-}" == 24.04 ) ]] || die '本脚本仅支持 Ubuntu 22.04 / 24.04。'
command -v flock >/dev/null || die '缺少 Ubuntu 基础命令 flock，请先安装 util-linux。'

secure_dir() {
    local path=$1 node='' part mode
    local -a parts
    IFS='/' read -r -a parts <<< "$path"
    for part in "${parts[@]}"; do
        [[ -n "$part" ]] || continue
        node+="/$part"
        [[ ! -L "$node" ]] || die '安装路径不可经过符号链接。'
        if [[ ! -e "$node" ]]; then mkdir -m 700 -- "$node"; fi
        [[ -d "$node" && "$(stat -c %u -- "$node")" == 0 ]] || die '安装路径必须为 root 拥有的目录。'
        mode=$(stat -c %a -- "$node")
        (( (8#$mode & 0022) == 0 )) || die '安装路径不可由组用户或其他用户写入。'
    done
}
secure_dir /run/tokenav-gateway-lite-install
exec 9>/run/tokenav-gateway-lite-install/lock
flock -n 9 || die '另一份安装 / 更新正在执行，请待其结束后重试。'
secure_dir "$install_dir"
readonly env_file="$install_dir/.env"
[[ ! -L "$env_file" && ( ! -e "$env_file" || -f "$env_file" ) ]] || die '.env 必须为普通文件，不能是符号链接。'
if [[ -e "$env_file" ]]; then
    [[ "$(stat -c %u -- "$env_file")" == 0 && "$(stat -c %a -- "$env_file")" == 600 ]] || die '已有 .env 必须归 root 所有且权限为 600；请人工确认权限后重试。'
fi
[[ ! -e "$install_dir/current" || -L "$install_dir/current" ]] || die 'current 已存在且不是版本链接；请勿覆盖已有目录。'
secure_dir "$install_dir/releases"
secure_dir "$install_dir/data"
secure_dir "$install_dir/logs"
scratch=$(mktemp -d "$install_dir/.install.XXXXXXXX")
run_log=$(mktemp "$install_dir/logs/install-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXXXX.log")

compose() {
    docker --host unix:///var/run/docker.sock compose \
        --project-name "$PROJECT" --env-file "$env_file" -f "$source_dir/compose.yaml" "$@"
}
show_logs_command() {
    [[ -n "$source_dir" ]] || return 0
    printf '查看容器日志（日志可能含私密信息，请勿原样公开）：\n  sudo docker --host unix:///var/run/docker.sock compose --project-name %q --env-file %q -f %q logs --tail 100 gateway postgres redis\n' \
        "$PROJECT" "$env_file" "$source_dir/compose.yaml" >&2
}
finish() {
    local code=$?
    trap - EXIT
    # 只清理本次 mktemp 创建的工作目录；版本目录、配置及持久数据一律保留。
    if [[ -n "$scratch" && "$scratch" == "$install_dir"/.install.* ]]; then rm -rf -- "$scratch"; fi
    if (( code != 0 )); then
        printf '安装 / 更新未完成，已有配置、持久数据和 current 指向均保留。\n本次日志：%s（仅 root 可读）\n' "$run_log" >&2
        show_logs_command
    fi
    exit "$code"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 始终使用本机 daemon（容器服务），避免环境中的远端 Docker 或同名变量覆盖 .env。
unset DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH
unset APP_PORT LISTEN_ADDR ADMIN_EMAIL ADMIN_PASSWORD POSTGRES_PASSWORD REDIS_PASSWORD GATEWAY_DATA_DIR
unset ANTIGRAVITY_OAUTH_CLIENT_ID ANTIGRAVITY_OAUTH_CLIENT_SECRET GEMINI_CLI_OAUTH_CLIENT_ID GEMINI_CLI_OAUTH_CLIENT_SECRET
unset COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES
export DEBIAN_FRONTEND=noninteractive
package_installed() { [[ "$(dpkg-query -W -f='${Status}' "$1" 2>/dev/null || true)" == 'install ok installed' ]]; }
missing=()
for package in ca-certificates curl openssl jq; do
    package_installed "$package" || missing+=("$package")
done
if (( ${#missing[@]} )); then
    say '安装缺失的下载和校验工具……'
    apt-get update >>"$run_log" 2>&1
    apt-get install -y --no-install-recommends --no-remove --no-upgrade "${missing[@]}" >>"$run_log" 2>&1
fi

fetch() { curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 600 --retry 3 --output "$2" "$1" >>"$run_log" 2>&1; }
ensure_docker_source() {
    local source arch
    for source in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
        if [[ -f "$source" ]] && grep -Eq '^[^#]*https://download\.docker\.com/linux/ubuntu' "$source"; then return; fi
    done
    local key='/etc/apt/keyrings/tokenav-docker.asc' list='/etc/apt/sources.list.d/tokenav-docker.sources'
    [[ ! -e "$key" && ! -L "$key" && ! -e "$list" && ! -L "$list" ]] || die '安装器专用 Docker apt 源文件已存在，请检查后重试；脚本不会覆盖它们。'
    fetch 'https://download.docker.com/linux/ubuntu/gpg' "$scratch/docker.asc" || die '下载 Docker 官方签名密钥失败。'
    grep -q '^-----BEGIN PGP PUBLIC KEY BLOCK-----' "$scratch/docker.asc" || die 'Docker 官方签名密钥格式异常。'
    [[ -d /etc/apt/keyrings ]] || install -d -m 755 /etc/apt/keyrings
    arch=$(dpkg --print-architecture)
    printf 'Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: %s\n' \
        "$VERSION_CODENAME" "$arch" "$key" >"$scratch/docker.sources"
    install -m 644 "$scratch/docker.asc" "$key"
    install -m 644 "$scratch/docker.sources" "$list"
}

daemon_exists=0
if command -v dockerd >/dev/null || package_installed docker-ce || package_installed docker.io || systemctl cat docker.service >/dev/null 2>&1; then daemon_exists=1; fi
missing=()
if (( daemon_exists == 0 )); then
    # 不卸载宿主已有的 containerd/runc 或非标准 Docker 安装。
    if command -v docker >/dev/null || command -v containerd >/dev/null || package_installed containerd || package_installed runc; then
        die '发现其他 Docker / 容器运行时安装但未识别到本机 daemon；请人工补齐 Docker，脚本不会替换。'
    fi
    missing+=(docker-ce docker-ce-cli containerd.io)
else
    command -v docker >/dev/null || die '已有 Docker daemon 但缺少命令行工具，请按原安装方式补齐。'
fi
if ! docker compose version >/dev/null 2>&1; then missing+=(docker-compose-plugin); fi
if ! docker buildx version >/dev/null 2>&1; then missing+=(docker-buildx-plugin); fi
if (( ${#missing[@]} )); then
    say '通过 Docker 官方 apt 源安装缺失组件……'
    ensure_docker_source
    apt-get update >>"$run_log" 2>&1
    apt-get -s install --no-install-recommends --no-remove --no-upgrade "${missing[@]}" >"$scratch/apt-plan" 2>>"$run_log"
    # 模拟事务后再安装；缺失插件不能成为替换现有 daemon / 运行时的理由。
    while read -r action package rest; do
        case "$action:$package" in
            Remv:*) die '安装依赖将删除已有软件包，已停止；请人工处理 Docker 依赖。' ;;
            Inst:docker-ce|Inst:docker-ce-cli|Inst:docker.io|Inst:containerd|Inst:containerd.io|Inst:runc)
                package_installed "$package" && die '安装依赖将更新已有 Docker / 容器运行时，已停止；请按原安装方式补齐插件。' ;;
        esac
    done <"$scratch/apt-plan"
    apt-get install -y --no-install-recommends --no-remove --no-upgrade "${missing[@]}" >>"$run_log" 2>&1
fi
if ! docker --host unix:///var/run/docker.sock info >/dev/null 2>&1; then
    say '启动本机 Docker 服务……'
    systemctl start docker >>"$run_log" 2>&1 || die '无法启动 Docker；请检查 systemctl status docker。'
fi
docker --host unix:///var/run/docker.sock info >/dev/null 2>>"$run_log" || die '本机 Docker 服务尚不可用。'
docker compose version >/dev/null 2>>"$run_log" || die 'Docker Compose 插件不可用。'
docker buildx version >/dev/null 2>>"$run_log" || die 'Docker Buildx 构建插件不可用。'

containers=$(docker --host unix:///var/run/docker.sock ps -aq --filter "label=com.docker.compose.project=$PROJECT")
volumes=$(docker --host unix:///var/run/docker.sock volume ls -q --filter "label=com.docker.compose.project=$PROJECT")
if [[ ! -f "$env_file" ]]; then
    [[ -z "$containers" && -z "$volumes" ]] || die '发现该项目已有容器或数据卷，但本目录缺少 .env；请找回原配置，不可重新生成数据库密码。'
    [[ -z "$(find "$install_dir/data" -mindepth 1 -maxdepth 1 -print -quit)" ]] || die '已有网关数据但缺少 .env，请先恢复原配置。'
fi
for container in $containers; do
    config_path=$(docker --host unix:///var/run/docker.sock inspect --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$container")
    case "$config_path" in
        "$install_dir"/releases/*/compose.yaml|"$install_dir"/current/compose.yaml) ;;
        *) die '该 Compose 项目正由另一个目录管理；请使用原 --dir，不能创建第二套安装。' ;;
    esac
done

say '解析 main 分支的固定提交并下载源码……'
fetch "https://api.github.com/repos/$REPOSITORY/commits/main" "$scratch/commit.json" || die '解析 GitHub main 失败（可能为网络、限流或仓库尚未公开）；未使用其他版本回退。'
commit=$(jq -er '.sha | select(type == "string")' "$scratch/commit.json" 2>>"$run_log") || die 'GitHub 未返回有效提交。'
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die 'GitHub 提交号格式异常。'
export SOURCE_COMMIT="$commit"
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
export BUILD_DATE
source_dir="$install_dir/releases/$commit"
if [[ -e "$source_dir" || -L "$source_dir" ]]; then
    secure_dir "$source_dir"
else
    fetch "https://codeload.github.com/$REPOSITORY/tar.gz/$commit" "$scratch/source.tar.gz" || die '下载固定提交源码失败，保留现有版本。'
    tar -tzf "$scratch/source.tar.gz" >"$scratch/archive-files" 2>>"$run_log" || die '源码压缩包无效。'
    while IFS= read -r member; do
        [[ "$member" == "tokenav-gateway-lite-$commit/"* && "$member" != *'/../'* && "$member" != *'/./'* ]] || die '源码压缩包包含异常路径。'
    done <"$scratch/archive-files"
    mkdir -m 700 "$scratch/source"
    tar -xzf "$scratch/source.tar.gz" --strip-components=1 --no-same-owner --no-same-permissions -C "$scratch/source" >>"$run_log" 2>&1
    [[ -f "$scratch/source/compose.yaml" && -f "$scratch/source/Dockerfile" && ! -e "$scratch/source/.git" ]] || die '源码缺少 compose.yaml / Dockerfile 或意外包含 Git 元数据。'
    mv -T -- "$scratch/source" "$source_dir"
fi
[[ -f "$source_dir/compose.yaml" && -f "$source_dir/Dockerfile" && ! -e "$source_dir/.git" ]] || die '版本目录不完整，请保留该目录并人工检查。'
say "源码提交：$commit"

if [[ ! -f "$env_file" ]]; then
    new_admin_password=$(openssl rand -hex 24)
    postgres_password=$(openssl rand -hex 32)
    redis_password=$(openssl rand -hex 32)
    printf 'APP_PORT=%s\nLISTEN_ADDR=0.0.0.0\nADMIN_EMAIL=admin@localhost.invalid\nADMIN_PASSWORD=%s\nPOSTGRES_PASSWORD=%s\nREDIS_PASSWORD=%s\nGATEWAY_DATA_DIR=%s/data\n' \
        "$requested_port" "$new_admin_password" "$postgres_password" "$redis_password" "$install_dir" >"$scratch/new.env"
    chmod 600 "$scratch/new.env"
    # 硬链接保证原子创建，任何情况下都不覆盖已有 .env。
    ln -- "$scratch/new.env" "$env_file"
    unset postgres_password redis_password
    # /dev/tty 文件可写不代表当前进程有控制终端，重定向失败也不能中断安装。
    if { printf '\n首次管理员：admin@localhost.invalid\n首次密码：%s\n请立即保存；此密码仅显示在当前终端，不写入安装日志。\n\n' "$new_admin_password" >/dev/tty; } 2>/dev/null; then
        :
    else
        say '当前没有终端，未输出首次管理员密码；请由 root 在本机私密查看 .env 中的 ADMIN_PASSWORD。'
    fi
    unset new_admin_password
else
    say '沿用已有 .env 和数据库，不重置管理员或数据库密码。'
fi

# 使用 Compose 自己的解析器校验配置，避免 source/eval 执行 .env 或误解引号。
compose config --format json >"$scratch/compose.json" 2>>"$run_log"
jq -e '.services | has("gateway") and has("postgres") and has("redis")' "$scratch/compose.json" >/dev/null || die 'Compose 缺少约定服务。'
app_port=$(jq -er '.services.gateway.ports | select(length == 1) | .[0] | select(.target == 8080) | .published' "$scratch/compose.json") || die '网关宿主端口配置异常。'
listen_addr=$(jq -er '.services.gateway.ports[0].host_ip // "0.0.0.0"' "$scratch/compose.json")
data_dir=$(jq -er '[.services.gateway.volumes[] | select(.type == "bind" and .target == "/app/data")][0].source' "$scratch/compose.json")
valid_port "$app_port" || die '已有 APP_PORT 不是有效端口，请检查 .env。'
[[ "$data_dir" == "$install_dir/data" ]] || die 'GATEWAY_DATA_DIR 必须为当前安装目录的 data 绝对路径；脚本不会移动已有数据。'
[[ "$listen_addr" =~ ^[0-9a-fA-F:.]+$ ]] || die 'LISTEN_ADDR 必须为本机 IPv4 / IPv6 地址。'
if (( port_supplied && 10#$app_port != requested_port )); then die '--port 与已有配置不同；已有 .env 未修改，请沿用原端口或人工修改 APP_PORT。'; fi
case "$listen_addr" in
    0.0.0.0) health_host='127.0.0.1' ;;
    ::) health_host='[::1]' ;;
    *:*) health_host="[$listen_addr]" ;;
    *) health_host=$listen_addr ;;
esac

say "构建网关和 Win12 内嵌前端，耗时取决于网络与机器配置。本次日志：$run_log"
compose build gateway >>"$run_log" 2>&1
say '启动网关、PostgreSQL 和 Redis……'
compose up -d --no-build >>"$run_log" 2>&1
say '从宿主检查 /ready（最多约 5 分钟，不要求预先绑定主站）……'
ready=0
for (( attempt=0; attempt<60; attempt++ )); do
    if curl --fail --silent --noproxy '*' --connect-timeout 2 --max-time 3 \
        --output "$scratch/ready.json" "http://$health_host:$app_port/ready" \
        && jq -e '.status == "ready" and .checks.database == "ok" and .checks.redis == "ok"' \
            "$scratch/ready.json" >/dev/null 2>&1; then ready=1; break; fi
    sleep 2
done
(( ready == 1 )) || die '/ready 检查未通过；新容器可能已启动，未自动回退数据库或切换 current，请查看日志后重试。'
# 先验证再原子替换链接；失败时保留上次成功源码及所有数据，不自动降级数据库。
ln -s -- "$source_dir" "$scratch/current"
mv -Tf -- "$scratch/current" "$install_dir/current"
say "安装 / 更新成功。源码：$source_dir"
say "后台入口：http://服务器地址:$app_port/gateway-admin"
say '请登录后按提示填写主站地址与 ge_ 对接密钥；/ready 成功仅代表数据库和 Redis 已就绪。'
