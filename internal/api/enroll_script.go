package api

// enrollScript is served at /enroll.sh and pasted onto a new node.
//
// It is strict POSIX sh: Alpine ships busybox ash with no bash, and these are
// exactly the machines most likely to be enrolled. Every external tool it uses
// (curl or wget, awk, grep) exists on a minimal install of every supported
// distribution.
const enrollScript = `#!/bin/sh
# fluxlite node enrollment
set -eu

PANEL="${1-}"
TOKEN="${2-}"

say()  { printf '%s\n' "$*"; }
step() { printf '\033[36m[%s]\033[0m %s\n' "$1" "$2"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

[ -n "$PANEL" ] && [ -n "$TOKEN" ] || die "用法: enroll.sh <面板地址> <令牌>"
[ "$(id -u)" = "0" ] || die "需要 root 权限执行"

say ""
say "fluxlite 节点注册"
say "面板: $PANEL"
say ""

# ---------- HTTP ----------
if command -v curl >/dev/null 2>&1; then
    http_get()  { curl -fsSL --max-time 60 "$1"; }
    http_save() { curl -fsSL --max-time 600 -o "$2" "$1"; }
    http_post() { curl -fsS --max-time 300 -X POST -H 'Content-Type: application/json' --data "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    http_get()  { wget -qO- --timeout=60 "$1"; }
    http_save() { wget -qO "$2" --timeout=600 "$1"; }
    http_post() { wget -qO- --timeout=300 --header='Content-Type: application/json' --post-data="$2" "$1"; }
else
    die "需要 curl 或 wget，请先安装其中之一"
fi

# ---------- 系统识别 ----------
step 1/5 "识别系统"

case "$(uname -m)" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "不支持的 CPU 架构: $(uname -m)（仅支持 x86_64 与 aarch64）" ;;
esac

OSID=unknown
if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OSID="${ID:-unknown}"
fi

if [ -d /run/systemd/system ]; then
    INIT=systemd
elif command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ]; then
    INIT=openrc
else
    die "不支持的 init 系统（需要 systemd 或 OpenRC）"
fi

ok "$OSID / $ARCH / $INIT"

# ---------- 安装公钥 ----------
step 2/5 "安装面板公钥"

KEY="$(http_get "$PANEL/api/enroll/key?token=$TOKEN")" || die "获取公钥失败：令牌无效或已过期"
[ -n "$KEY" ] || die "面板返回了空公钥"

SSH_HOME="$(awk -F: -v u="$(id -un)" '$1==u{print $6}' /etc/passwd 2>/dev/null || true)"
[ -n "$SSH_HOME" ] || SSH_HOME="${HOME:-/root}"

mkdir -p "$SSH_HOME/.ssh"
chmod 700 "$SSH_HOME/.ssh"
AUTH="$SSH_HOME/.ssh/authorized_keys"
[ -f "$AUTH" ] || : > "$AUTH"
chmod 600 "$AUTH"

if grep -qF "$KEY" "$AUTH" 2>/dev/null; then
    ok "公钥已存在，跳过"
else
    # 某些系统的 authorized_keys 末尾没有换行，直接追加会与上一行粘连
    [ -s "$AUTH" ] && [ "$(tail -c 1 "$AUTH" | wc -l)" -eq 0 ] && printf '\n' >> "$AUTH"
    printf '%s\n' "$KEY" >> "$AUTH"
    ok "公钥已写入 $AUTH"
fi

# SELinux 系统上新建的 .ssh 目录标签不对会让 sshd 拒绝读取
if command -v restorecon >/dev/null 2>&1; then
    restorecon -R "$SSH_HOME/.ssh" 2>/dev/null || true
fi

# ---------- sshd 前置条件 ----------
step 3/5 "检查 sshd 配置"

SSHD_CONF=/etc/ssh/sshd_config
if [ -r "$SSHD_CONF" ]; then
    if grep -qiE '^[[:space:]]*PubkeyAuthentication[[:space:]]+no' "$SSHD_CONF"; then
        warn "sshd 禁用了公钥认证 (PubkeyAuthentication no)，面板将无法登录"
        warn "请改为 yes 后执行: systemctl reload sshd  或  rc-service sshd reload"
    fi
    if [ "$(id -un)" = "root" ] && grep -qiE '^[[:space:]]*PermitRootLogin[[:space:]]+(no|prohibit-password|without-password)' "$SSHD_CONF"; then
        if grep -qiE '^[[:space:]]*PermitRootLogin[[:space:]]+no' "$SSHD_CONF"; then
            warn "sshd 禁止 root 登录 (PermitRootLogin no)，面板将无法登录"
        else
            ok "sshd 仅允许 root 密钥登录，符合本次配置"
        fi
    fi
    ok "sshd 配置检查完成"
else
    warn "未找到 $SSHD_CONF，跳过检查"
fi

# ---------- 安装 realm ----------
step 4/5 "安装转发内核 realm"

REALM_VER=""
if [ -x /usr/local/bin/realm ]; then
    REALM_VER="$(/usr/local/bin/realm --version 2>/dev/null | awk '{print $2}' || true)"
fi

if [ -n "$REALM_VER" ]; then
    ok "已安装 realm $REALM_VER，跳过下载"
else
    # 二进制由面板下发，节点无需访问 GitHub —— 对国内机器这一步经常是唯一的卡点
    http_save "$PANEL/api/enroll/realm?token=$TOKEN&arch=$ARCH" /tmp/.realm.new \
        || die "从面板下载 realm 失败"
    chmod +x /tmp/.realm.new
    mv -f /tmp/.realm.new /usr/local/bin/realm
    REALM_VER="$(/usr/local/bin/realm --version 2>/dev/null | awk '{print $2}' || true)"
    [ -n "$REALM_VER" ] || die "realm 安装后无法执行，可能架构不匹配"
    ok "realm $REALM_VER 已安装"
fi

mkdir -p /etc/fluxlite/realm /var/log/fluxlite
ok "工作目录就绪"

# ---------- 上报 ----------
step 5/5 "向面板注册"

HOSTNAME_VAL="$(hostname 2>/dev/null || echo unknown)"
PAYLOAD="$(printf '{"token":"%s","arch":"%s","os_id":"%s","init_system":"%s","realm_version":"%s","hostname":"%s"}' \
    "$TOKEN" "$ARCH" "$OSID" "$INIT" "$REALM_VER" "$HOSTNAME_VAL")"

RESP="$(http_post "$PANEL/api/enroll/report" "$PAYLOAD")" || die "上报失败，请检查网络与令牌有效期"

say ""
if printf '%s' "$RESP" | grep -q '"verified":true'; then
    NODE_NAME="$(printf '%s' "$RESP" | sed -n 's/.*"name":"\([^"]*\)".*/\1/p')"
    UDP="$(printf '%s' "$RESP" | sed -n 's/.*"udp_status":"\([^"]*\)".*/\1/p')"
    printf '\033[32m注册成功\033[0m — 节点 %s 已就绪\n' "$NODE_NAME"
    say "面板已成功回连本机，UDP 穿透: ${UDP:-未知}"
    say ""
    say "现在可以在面板的「链路」页把这台机器加入转发链了。"
else
    DETAIL="$(printf '%s' "$RESP" | sed -n 's/.*"detail":"\([^"]*\)".*/\1/p')"
    printf '\033[33m节点已登记，但面板回连失败\033[0m\n'
    say "原因: ${DETAIL:-未知}"
    say ""
    say "常见原因:"
    say "  · 填写的连接地址或端口与本机实际入口不符（NAT 机器需填服务商映射后的地址）"
    say "  · 防火墙/安全组未放行该 SSH 端口"
    say "  · sshd 不允许公钥登录或不允许该用户登录"
    say ""
    say "修正后可在面板节点页点「探测」重试，无需重新执行本命令。"
fi
say ""
`
