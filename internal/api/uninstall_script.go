package api

// uninstallScript is served at /uninstall.sh and pasted onto a node that is
// being taken out of the fleet.
//
// It carries no token. Deleting a node's record in the panel does not reach
// the machine, so the panel's key would otherwise sit in authorized_keys
// forever — root access to a host nobody is managing any more. That has to be
// removable from the node itself, including when the panel cannot reach it.
//
// Strict POSIX sh for the same reason as the installer: Alpine ships busybox
// ash and no bash.
const uninstallScript = `#!/bin/sh
# fluxlite node uninstall
set -eu

say()  { printf '%s\n' "$*"; }
step() { printf '\033[36m[%s]\033[0m %s\n' "$1" "$2"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die()  { printf '  \033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

PURGE_REALM=0
KEEP_SESSIONS=0
for arg in "$@"; do
    case "$arg" in
        --purge-realm) PURGE_REALM=1 ;;
        --keep-sessions) KEEP_SESSIONS=1 ;;
        -h|--help)
            say "用法: uninstall.sh [--purge-realm] [--keep-sessions]"
            say "  --purge-realm    即使别处仍在引用，也删除 /usr/local/bin/realm"
            say "  --keep-sessions  不断开其他 SSH 会话（面板可能借旧连接继续管理本机）"
            exit 0 ;;
        *) die "未知参数: $arg" ;;
    esac
done

[ "$(id -u)" = "0" ] || die "需要 root 权限执行"

say ""
say "fluxlite 节点卸载"
say ""

# ---------- 停止并移除服务 ----------
step 1/5 "停止转发服务"

STOPPED=0
if [ -d /run/systemd/system ]; then
    # 运行中的实例和开机自启的软链是两份来源，都要收集：只看其一会漏掉
    # 「已启用但当前没跑」或「在跑但没启用」的实例。
    UNITS="$(
        {
            systemctl list-units --all --plain --no-legend 'fluxlite-relay@*' 2>/dev/null | awk '{print $1}'
            ls /etc/systemd/system/multi-user.target.wants/ 2>/dev/null | grep '^fluxlite-relay@' || true
        } | sed 's/\.service$//' | sort -u
    )"
    for unit in $UNITS; do
        [ -n "$unit" ] || continue
        systemctl stop "$unit" 2>/dev/null || true
        systemctl disable "$unit" 2>/dev/null || true
        ok "已停止 $unit"
        STOPPED=$((STOPPED + 1))
    done
    if [ -f /etc/systemd/system/fluxlite-relay@.service ]; then
        rm -f /etc/systemd/system/fluxlite-relay@.service
        ok "已删除 systemd 模板单元"
    fi
    systemctl daemon-reload 2>/dev/null || true
else
    for script in /etc/init.d/fluxlite-*; do
        [ -e "$script" ] || continue
        name="$(basename "$script")"
        rc-service "$name" stop 2>/dev/null || true
        rc-update del "$name" default 2>/dev/null || true
        rm -f "$script"
        ok "已停止并删除 $name"
        STOPPED=$((STOPPED + 1))
    done
fi
[ "$STOPPED" -gt 0 ] || warn "没有找到 fluxlite 的转发服务"

# ---------- 移除配置与日志 ----------
step 2/5 "删除配置与日志"

for path in /etc/fluxlite /var/log/fluxlite; do
    if [ -e "$path" ]; then
        rm -rf "$path"
        ok "已删除 $path"
    fi
done

# supervise-daemon 的 pid 文件在 /run 下，正常停止时会自己清掉，异常退出时会残留。
for pidfile in /run/fluxlite-*.pid; do
    [ -e "$pidfile" ] || continue
    rm -f "$pidfile"
    ok "已删除 $pidfile"
done

# ---------- 撤销面板公钥 ----------
step 3/5 "撤销面板的登录公钥"

# 面板下发的公钥注释固定为 fluxlite-<节点名>。只按这个注释匹配，绝不整行清空：
# 同一个 authorized_keys 里通常还有机主自己的密钥，删错就把人锁在门外了。
revoke_keys() {
    f="$1"
    [ -f "$f" ] || return 0
    awk '$3 ~ /^fluxlite-/ { found = 1 } END { exit !found }' "$f" || return 0

    backup="$f.fluxlite-removed-$(date +%Y%m%d%H%M%S)"
    cp "$f" "$backup"
    awk '!($3 ~ /^fluxlite-/)' "$f" > "$f.fluxlite-tmp"
    mv "$f.fluxlite-tmp" "$f"
    chmod 600 "$f"

    removed="$(( $(wc -l < "$backup") - $(wc -l < "$f") ))"
    ok "$f 移除 $removed 条，备份在 $backup"
    if [ ! -s "$f" ]; then
        warn "$f 现在是空的 —— 如果这台机器不允许密码登录，请先确认你还有别的进入方式"
    fi
}

FOUND_KEY=0
for home in /root /home/*; do
    [ -d "$home" ] || continue
    if [ -f "$home/.ssh/authorized_keys" ] &&
       awk '$3 ~ /^fluxlite-/ { found = 1 } END { exit !found }' "$home/.ssh/authorized_keys"; then
        FOUND_KEY=1
    fi
    revoke_keys "$home/.ssh/authorized_keys"
done
[ "$FOUND_KEY" = "1" ] || warn "没有找到面板下发的公钥"

# ---------- 断开面板残留的连接 ----------
step 4/5 "断开面板已建立的 SSH 会话"

# 撤销公钥只影响之后的登录 —— SSH 仅在建连时校验授权，已经建立的会话不受影响。
# 面板对每个节点保持一条长连接，不断开的话它会继续管理本机：只要还有链路经过
# 这台机器，巡检就会在几分钟内把 realm 和配置原样装回来。
drop_other_sessions() {
    # 自己这条会话的进程链绝不能杀，否则脚本会把自己掐断。
    mine=" "
    pid=$$
    while [ "$pid" -gt 1 ] 2>/dev/null; do
        mine="$mine$pid "
        parent="$(awk '/^PPid:/ { print $2 }' "/proc/$pid/status" 2>/dev/null || true)"
        [ -n "$parent" ] || break
        pid="$parent"
    done

    killed=0
    for entry in /proc/[0-9]*; do
        p="${entry#/proc/}"
        case "$mine" in *" $p "*) continue ;; esac
        # 命令替换会丢掉 NUL 分隔符，正好把 cmdline 拼成一串。
        cmd="$(cat "$entry/cmdline" 2>/dev/null || true)"
        # 每条会话的 sshd 进程标题形如 "sshd: root@notty"。监听进程和特权父进程
        # 不含 @，所以匹配 @ 就不会误伤它们。
        case "$cmd" in
            "sshd: "*@*|"sshd-session: "*@*) ;;
            *) continue ;;
        esac
        kill "$p" 2>/dev/null && killed=$((killed + 1))
    done
    printf '%s' "$killed"
}

if [ "$KEEP_SESSIONS" = "1" ]; then
    warn "按 --keep-sessions 跳过；面板可能仍借旧连接管理本机"
else
    DROPPED="$(drop_other_sessions)"
    if [ "$DROPPED" -gt 0 ]; then
        ok "已断开 $DROPPED 条其他 SSH 会话（本次会话不受影响）"
    else
        ok "没有其他 SSH 会话"
    fi
fi

# ---------- 转发内核 ----------
step 5/5 "处理转发内核 realm"

REALM=/usr/local/bin/realm
if [ ! -e "$REALM" ]; then
    ok "realm 未安装，跳过"
elif [ "$PURGE_REALM" = "1" ]; then
    rm -f "$REALM"
    ok "已删除 $REALM（--purge-realm）"
else
    # fluxlite 的单元此时已经删掉了，所以还引用它的一定是别人 —— 手工配置的
    # realm.service、别的面板装的，删掉会静默弄坏那些转发。
    OTHER="$(grep -rls "$REALM" /etc/systemd/system /etc/init.d /usr/lib/systemd/system 2>/dev/null | head -3 || true)"
    RUNNING="$(pgrep -f "$REALM" 2>/dev/null | head -1 || true)"
    if [ -n "$OTHER" ] || [ -n "$RUNNING" ]; then
        warn "保留 $REALM —— 还有别的东西在用它："
        [ -n "$OTHER" ] && printf '      %s\n' $OTHER
        [ -n "$RUNNING" ] && printf '      仍有 realm 进程在运行 (pid %s)\n' "$RUNNING"
        say "      确认无用后可执行: rm -f $REALM"
    else
        rm -f "$REALM"
        ok "已删除 $REALM"
    fi
fi

say ""
printf '\033[32m卸载完成\033[0m\n'
say ""
say "本机已不再受面板管理，面板会在一个巡检周期内把它标记为离线。"
say "确认离线后，到面板的节点页删除对应记录即可。"
say ""
say "提示：如果面板里还有链路经过本机，请先在面板上停止或删除那些链路，"
say "      否则删不掉节点记录。"
say ""
say "本脚本不碰 iptables —— fluxlite 从不创建转发规则，它只跑 realm 进程。"
say "机器上如果有 DNAT/端口转发规则，那是别的工具建的，请用那个工具清理。"
say ""
`
