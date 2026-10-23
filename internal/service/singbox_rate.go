package service

import (
	"context"
	"fmt"
	"strconv"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

func singBoxLinuxUser(id int64) string { return fmt.Sprintf("fluxlite-sb-%d", id) }

func ensureSingBoxLinuxUser(ctx context.Context, client *ssh.Client, id int64) error {
	name := shellQuote(singBoxLinuxUser(id))
	cmd := "set -e\nif ! id -u " + name + " >/dev/null 2>&1; then if command -v useradd >/dev/null 2>&1; then useradd --system --no-create-home --shell /usr/sbin/nologin " + name + "; elif command -v adduser >/dev/null 2>&1; then adduser -S -D -H -s /sbin/nologin " + name + "; else echo '无法创建 sing-box 独立系统用户' >&2; exit 1; fi; fi"
	_, err := sshx.RunCheck(ctx, client, cmd)
	return err
}

func ensureSingBoxServiceIdentity(ctx context.Context, client *ssh.Client, node *model.Node, u *model.SingBoxUser) error {
	user := shellQuote(singBoxLinuxUser(u.ID))
	dir := fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID)
	data := "/var/lib/fluxlite-singbox/" + strconv.FormatInt(u.ID, 10)
	if _, err := sshx.RunCheck(ctx, client, "chown -R "+user+":"+user+" "+shellQuote(dir)+" "+shellQuote(data)); err != nil {
		return err
	}
	if node.InitSystem == model.InitSystemd {
		dropin := "/etc/systemd/system/" + u.ServiceName + ".service.d/10-fluxlite-user.conf"
		if err := sshx.WriteFile(ctx, client, dropin, []byte("[Service]\nUser="+singBoxLinuxUser(u.ID)+"\nGroup="+singBoxLinuxUser(u.ID)+"\n"), "0644"); err != nil {
			return err
		}
		cmd := "systemctl daemon-reload"
		if u.Enabled {
			cmd += " && systemctl restart " + shellQuote(u.ServiceName) + ".service"
		}
		_, err := sshx.RunCheck(ctx, client, cmd)
		return err
	}
	if node.InitSystem == model.InitOpenRC {
		path := "/etc/init.d/" + u.ServiceName
		cmd := "if grep -q '^command_user=' " + shellQuote(path) + "; then sed -i 's/^command_user=.*/command_user=\"" + singBoxLinuxUser(u.ID) + "\"/' " + shellQuote(path) + "; else sed -i '/^description=/a command_user=\"" + singBoxLinuxUser(u.ID) + "\"' " + shellQuote(path) + "; fi"
		if u.Enabled {
			cmd += " && rc-service " + shellQuote(u.ServiceName) + " restart"
		}
		_, err := sshx.RunCheck(ctx, client, cmd)
		return err
	}
	return fmt.Errorf("unsupported init system %q", node.InitSystem)
}

func applySingBoxRateLimit(ctx context.Context, client *ssh.Client, u *model.SingBoxUser) error {
	chain := fmt.Sprintf("FLUXLITE_SB_RATE_%d", u.ID)
	mark := 50000 + u.ID%10000
	pref := 42000 + u.ID%1000
	if u.UploadLimitBps == 0 && u.DownloadLimitBps == 0 {
		return removeSingBoxRateLimit(ctx, client, u.ID)
	}
	user := shellQuote(singBoxLinuxUser(u.ID))
	cmd := fmt.Sprintf("set -e\ncommand -v tc >/dev/null 2>&1 || { echo 'tc 未安装' >&2; exit 20; }\niface=$(ip route show default 2>/dev/null | awk 'NR==1{print $5}')\n[ -n \"$iface\" ] || { echo '无法识别默认出口网卡' >&2; exit 21; }\nfor bin in iptables ip6tables; do command -v \"$bin\" >/dev/null 2>&1 || { echo \"$bin 未安装\" >&2; exit 22; }; \"$bin\" -t mangle -N %s 2>/dev/null || true; \"$bin\" -t mangle -F %s; \"$bin\" -t mangle -C OUTPUT -j %s 2>/dev/null || \"$bin\" -t mangle -I OUTPUT 1 -j %s; \"$bin\" -t mangle -C PREROUTING -j %s 2>/dev/null || \"$bin\" -t mangle -I PREROUTING 1 -j %s; \"$bin\" -t mangle -A %s -m owner --uid-owner %s -j MARK --set-mark %d; for p in tcp udp; do \"$bin\" -t mangle -A %s -p $p --dport %d -j MARK --set-mark %d; done; done\ntc qdisc add dev \"$iface\" clsact 2>/dev/null || true\n%s\n%s", chain, chain, chain, chain, chain, chain, chain, user, mark, chain, u.Port, mark, policeCommand("egress", pref, mark, u.UploadLimitBps), policeCommand("ingress", pref+1, mark, u.DownloadLimitBps))
	_, err := sshx.RunCheck(ctx, client, cmd)
	return err
}

func policeCommand(direction string, pref, mark, rate int64) string {
	if rate <= 0 {
		return fmt.Sprintf("tc filter del dev \"$iface\" %s pref %d 2>/dev/null || true", direction, pref)
	}
	return fmt.Sprintf("tc filter replace dev \"$iface\" %s protocol all pref %d handle %d fw action police rate %dbit burst 128kb drop", direction, pref, mark, rate)
}

func removeSingBoxRateLimit(ctx context.Context, client *ssh.Client, id int64) error {
	chain := fmt.Sprintf("FLUXLITE_SB_RATE_%d", id)
	pref := 42000 + id%1000
	cmd := fmt.Sprintf("iface=$(ip route show default 2>/dev/null | awk 'NR==1{print $5}')\nif [ -n \"$iface\" ] && command -v tc >/dev/null 2>&1; then tc filter del dev \"$iface\" egress pref %d 2>/dev/null || true; tc filter del dev \"$iface\" ingress pref %d 2>/dev/null || true; fi\nfor bin in iptables ip6tables; do command -v \"$bin\" >/dev/null 2>&1 || continue; \"$bin\" -t mangle -D OUTPUT -j %s 2>/dev/null || true; \"$bin\" -t mangle -D PREROUTING -j %s 2>/dev/null || true; \"$bin\" -t mangle -F %s 2>/dev/null || true; \"$bin\" -t mangle -X %s 2>/dev/null || true; done", pref, pref+1, chain, chain, chain, chain)
	_, err := sshx.RunCheck(ctx, client, cmd)
	return err
}

func rateLimitSummary(u *model.SingBoxUser) string {
	if u.UploadLimitBps == 0 && u.DownloadLimitBps == 0 {
		return "未限速"
	}
	return strconv.FormatInt(u.UploadLimitBps/1_000_000, 10) + "/" + strconv.FormatInt(u.DownloadLimitBps/1_000_000, 10) + " Mbps"
}
