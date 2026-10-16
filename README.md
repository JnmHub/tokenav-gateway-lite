# TokenAV 轻量网关

TokenAV 的区域网关，提供上游调度、请求转发、额度判断、用量回传及 Win12 风格管理后台。用户、余额和计费规则由绑定的主站管理。

公开源码仓库：[JnmHub/tokenav-gateway-lite](https://github.com/JnmHub/tokenav-gateway-lite)，默认分支为 `main`。这是独立源码仓库，仓库根目录即可构建。安装时通过 Docker 多阶段构建 Win12 前端，再以 Go `embed（资源嵌入）` 打入网关程序；**网关镜像在本机从源码构建**，不依赖预先发布的网关镜像。

## 一键安装

支持 **Ubuntu 22.04 / 24.04**，以 root 或具有 sudo 权限的用户执行。需要能访问 GitHub、Docker 官方软件源、容器镜像源及源码依赖源。建议至少 2 核 CPU、4 GB 内存，并为编译缓存、源码版本和数据库预留磁盘空间。

先下载脚本到文件，再执行（不使用管道向 bash 输入脚本）。普通用户会由脚本调用 sudo 提权，root 可直接运行。

```bash
installer=$(mktemp) && curl -fL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 --retry 3 https://raw.githubusercontent.com/JnmHub/tokenav-gateway-lite/main/install.sh -o "$installer" && bash "$installer"
```

若系统没有 curl，先执行 `sudo apt-get update && sudo apt-get install -y ca-certificates curl`；root 用户去掉 `sudo`。

可选端口示例（仅首次安装时生效）。

```bash
installer=$(mktemp) && curl -fL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 --retry 3 https://raw.githubusercontent.com/JnmHub/tokenav-gateway-lite/main/install.sh -o "$installer" && bash "$installer" --port 18080
```

仅支持 `--dir`、`--port`、`--help`。安装目录默认 `/opt/tokenav-gateway-lite`，也可在命令末尾追加 `--dir /srv/tokenav-gateway-lite`。路径必须为 root 管理的独立绝对目录，不含空格或符号链接。每台主机的本机 Docker 服务只管理一套名为 `tokenav-gateway-lite` 的安装，`--dir` 不是创建第二个实例或迁移数据的命令。

安装器会完成以下工作。

- 安装缺失的下载工具；从 Docker 官方 apt 源安装缺失的 Docker、Compose（容器编排）和 Buildx（镜像构建）组件。保留已有 Docker daemon（容器服务）及配置，只在需要时启动服务。遇到需要替换既有容器运行时的依赖冲突时退出，不卸载原安装。
- 通过 GitHub API（接口）将 `main` 解析为确定的提交号，再从 codeload 下载该提交的源码包。解析或下载失败即退出，不回退到浮动分支包；不克隆 Git 历史。
- 首次创建权限为 `600`、归 root 所有的 `.env`，用 OpenSSL 随机生成管理员、PostgreSQL（数据库）及 Redis（缓存服务）密码。只在当前终端显示首次管理员密码；数据库及 Redis 密码不输出。
- 从源码构建并启动 `gateway`、`postgres`、`redis` 三个服务。宿主访问 `/ready` 检查最多约 5 分钟；通过后才切换 `current` 版本链接。源码编译时间另计。

首次启动**无需预先创建 `config.yaml`**；安装器会准备可写的持久数据目录。只有网关端口对外公开，PostgreSQL 和 Redis 使用内部容器网络及命名卷。默认监听 `0.0.0.0:8080`；脚本不修改宿主防火墙、反向代理或 Docker 的配置。

## 首次登录与主站绑定

打开 `http://服务器地址:8080/gateway-admin`，自定义端口时替换 `8080`。

1. 默认管理员邮箱为 `admin@localhost.invalid`，密码为安装终端显示的随机值。请立即保存，并在登录后设置自己的账号资料和密码。
2. 按后台“连接主站”提示填写自己的 TokenAV 主站网址。
3. 从主站“系统设置”复制 `ge_` 开头的网关对接密钥，填写并验证。
4. 填写节点名称、对外网址和区域，点击绑定，等待主站握手及配置同步完成。对外网址应是主站和客户端能够访问的实际地址。

管理员密码只用于空数据库首次创建账号。重复安装或改动 `.env` 中的 `ADMIN_PASSWORD` 不会重置已有管理员。后台修改密码后，`.env` 中的初始值也不会自动更新。无交互终端时不会输出密码，可由 root 在本机私密查看 `.env` 的 `ADMIN_PASSWORD`；不要将整份文件贴到聊天或问题单中。

`/health` 表示进程存活，`/ready` 表示数据库和 Redis 已就绪。**安装就绪不等于已经绑定主站或已能处理付费请求**。主站地址和对接密钥均由使用者提供，发行包不内置。

公网使用前请配置 HTTPS（加密访问），按需要限制管理后台访问来源。若由同机反向代理接入，可人工把 `.env` 中的 `LISTEN_ADDR` 调整为 `127.0.0.1` 后重新运行安装命令。

## 目录与配置

```text
/opt/tokenav-gateway-lite/
├── .env                 # 私密运行配置，root:root、600
├── data/                # 网关可写数据、运行配置等本地文件
├── releases/<提交号>/    # 下载的各版本源码，无 .git 目录
├── current -> releases/<最近成功提交号>
└── logs/                # 仅 root 可读的安装 / 构建日志
```

网关的 `data/` 挂载到容器 `/app/data`。**绑定身份主要存储在 PostgreSQL 中**；数据库和 Redis 保存在 Docker 命名卷中，不在源码目录，也不在宿主 `data/` 目录中。只备份安装目录或 `data/` 不能保留完整绑定身份，也不能代替数据库和 Redis 备份。

`.env` 使用以下变量；安装器生成后不覆盖其内容。

| 变量 | 用途 |
| --- | --- |
| `APP_PORT` | 宿主网关端口，默认 `8080`；容器内仍使用 `8080` |
| `LISTEN_ADDR` | 宿主监听地址，默认 `0.0.0.0`，支持本机 IPv4 / IPv6 地址 |
| `ADMIN_EMAIL`、`ADMIN_PASSWORD` | 空库首次管理员，默认邮箱及随机密码 |
| `POSTGRES_PASSWORD`、`REDIS_PASSWORD` | 随机生成的数据库、缓存服务密码 |
| `GATEWAY_DATA_DIR` | 当前安装目录下 `data/` 的绝对路径 |

需要换端口时，以 root 修改已有 `.env` 中的 `APP_PORT`，再执行更新命令；已有配置与 `--port` 不一致时脚本会退出。不要只修改数据库密码环境变量来“改密”，它不会同步修改 PostgreSQL 已有数据库用户密码。不要删除 `.env` 后重新生成随机密码，否则将无法连接原数据库。

下列运维命令均以默认目录为例。自定义目录时同步替换 `--env-file` 和 `-f` 后的路径；固定保留 `--project-name tokenav-gateway-lite`。命令显式使用本机 Docker，避免操作其他 Docker 上下文。

```bash
sudo docker --host unix:///var/run/docker.sock compose --project-name tokenav-gateway-lite --env-file /opt/tokenav-gateway-lite/.env -f /opt/tokenav-gateway-lite/current/compose.yaml ps
```

## 可选的 Google OAuth 客户端

普通安装、后台登录和主站绑定不需要这些配置。使用 Antigravity 或 Gemini CLI / Google One 的 OAuth（第三方授权）账号时，需要在私密 `.env` 中额外配置对应的客户端 ID 和 Secret；公开源码、镜像不内置固定 Google 客户端凭据。

| 用途 | 私密环境变量 |
| --- | --- |
| Antigravity | `ANTIGRAVITY_OAUTH_CLIENT_ID`、`ANTIGRAVITY_OAUTH_CLIENT_SECRET` |
| Gemini CLI / Google One | `GEMINI_CLI_OAUTH_CLIENT_ID`、`GEMINI_CLI_OAUTH_CLIENT_SECRET` |

填写与目标服务及授权账号匹配的合法客户端凭据后，重新执行安装命令以更新容器配置。未配置时相关授权、换取令牌或刷新会明确报缺少配置，不影响其他类型账号。已导入的刷新令牌需要匹配原签发客户端，不能随意换成另一个客户端。

Gemini AI Studio 的自定义客户端仍使用原有 `gemini.oauth` 配置，和上表 CLI 客户端配置不是同一项。不要将真实值提交到仓库或问题单。

## 更新与失败处理

先备份，再重新执行上面相同的一键安装命令；自定义目录需带上原来的 `--dir`。脚本会重新解析 `main`，保留已有 `.env`、数据库、Redis 数据和网关数据。同一提交直接复用该版本目录，旧源码不会被删除。

更新先构建镜像，再重建必要的容器，期间可能短暂停机。健康检查成功后原子切换 `current`。失败时保留旧 `current`、新旧源码和持久数据，打印本次私密日志路径及对应新版本的 `docker compose logs` 命令。**此时容器可能已更新，数据库迁移也可能已执行**；保留旧源码不代表自动回滚了服务或数据库。请检查错误、修复后重复执行，不要直接盲目降级。

查看当前版本的容器日志。

```bash
sudo docker --host unix:///var/run/docker.sock compose --project-name tokenav-gateway-lite --env-file /opt/tokenav-gateway-lite/.env -f /opt/tokenav-gateway-lite/current/compose.yaml logs --tail 100 gateway postgres redis
```

首次安装未成功时尚无 `current`，应使用脚本提示的 `releases/<提交号>/compose.yaml`。安装日志和容器日志可能含配置细节，分享前请脱敏；脚本不会自动把它们打印出来。GitHub 限流或网络错误时，稍后重复安装即可。

## 备份

至少备份 `.env`、`data/`、PostgreSQL 数据及 Redis 数据。Redis 可能保存待回传用量等运行状态，不能一概当作可丢弃缓存。备份包含私密凭据，应限制权限、加密保存并复制到独立存储。

下面在 root 私密目录创建备份，先停网关以避免新增写入，用 `pg_dump` 导出数据库，再停 Redis 复制其完整持久化目录。命令结束后恢复服务；它会中断当前网关流量，应在维护窗口执行。

```bash
sudo bash <<'BACKUP'
set -euo pipefail
umask 077
backup=$(mktemp -d /root/tokenav-gateway-backup-XXXXXXXX)
compose() {
  docker --host unix:///var/run/docker.sock compose \
    --project-name tokenav-gateway-lite \
    --env-file /opt/tokenav-gateway-lite/.env \
    -f /opt/tokenav-gateway-lite/current/compose.yaml "$@"
}
trap 'compose start redis gateway >/dev/null' EXIT
compose stop -t 60 gateway
compose exec -T postgres sh -c 'export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup/postgres.dump"
compose stop -t 60 redis
mkdir "$backup/redis"
compose cp redis:/data/. "$backup/redis"
tar -C /opt/tokenav-gateway-lite -czf "$backup/runtime.tar.gz" .env data
readlink -f /opt/tokenav-gateway-lite/current > "$backup/source-path.txt"
printf '备份完成：%s\n' "$backup"
BACKUP
```

`postgres.dump` 为 PostgreSQL 自定义格式备份，可用 `pg_restore` 恢复。恢复时要同时使用匹配的 `.env`、网关文件和 Redis 备份，保持原 Compose 项目名；先在隔离环境验证恢复，避免覆盖仍有新写入的数据。不要仅复制正在运行的 PostgreSQL 卷目录作为数据库备份。

## 停止与恢复

停止服务并保留容器、配置和全部持久数据。

```bash
sudo docker --host unix:///var/run/docker.sock compose --project-name tokenav-gateway-lite --env-file /opt/tokenav-gateway-lite/.env -f /opt/tokenav-gateway-lite/current/compose.yaml stop
```

恢复服务。

```bash
sudo docker --host unix:///var/run/docker.sock compose --project-name tokenav-gateway-lite --env-file /opt/tokenav-gateway-lite/.env -f /opt/tokenav-gateway-lite/current/compose.yaml start
```

停止服务不等于删除数据。不要使用带 `-v` 的容器移除操作、卷清理或目录删除来处理更新问题；这些操作可能永久删除数据库或尚未结算的数据，本指南不提供清空数据命令。旧源码和构建缓存不会自动清理，请按磁盘占用及备份情况自行管理。

## 来源与许可证

本项目基于 [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api) 修改，保留原项目来源、版权和许可证声明，并加入 TokenAV 轻量网关及 Win12 后台相关改动。

仓库 [LICENSE](LICENSE) 为 GNU LGPL v3（GNU 宽通用公共许可证第三版）；LGPL v3 以 GNU GPL v3 条款为基础并附加许可，[COPYING](COPYING) 随附 GPL v3 完整文本，也可查阅 [GNU 官方条款](https://www.gnu.org/licenses/gpl-3.0.html)。分发、修改及再发布时应遵守相应 GPL / LGPL 义务，保留许可证、版权声明，并按要求提供对应源码。各文件及第三方依赖如有独立许可声明，以其声明为准。
