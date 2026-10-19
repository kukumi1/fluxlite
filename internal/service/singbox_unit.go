package service

import (
	"errors"
	"fmt"

	"github.com/kukumi1/fluxlite/internal/model"
)

// Availability changes include boot policy: stopping alone would resurrect an
// expired or disabled inbound after the next node reboot.
func singBoxUnitCommand(init model.InitSystem, u *model.SingBoxUser, action string) (string, error) {
	if u == nil || u.ID <= 0 || u.Source != model.SingBoxManaged || u.ServiceName != fmt.Sprintf("fluxlite-singbox-%d", u.ID) {
		return "", errors.New("refusing unit operation outside managed sing-box namespace")
	}
	if action != "start" && action != "stop" {
		return "", errors.New("unsupported sing-box unit action")
	}
	name := shellQuote(u.ServiceName)
	switch init {
	case model.InitSystemd:
		unit := shellQuote(u.ServiceName + ".service")
		if action == "start" {
			return "systemctl enable --now " + unit + " && systemctl is-enabled --quiet " + unit + " && systemctl is-active --quiet " + unit, nil
		}
		return "systemctl disable --now " + unit + " || exit 1\n" +
			"boot=$(systemctl show " + unit + " --property=UnitFileState --value) || exit 1\n" +
			"case \"$boot\" in disabled|masked) ;; *) exit 1;; esac\n" +
			"active=$(systemctl show " + unit + " --property=ActiveState --value) || exit 1\n" +
			"case \"$active\" in inactive|failed) ;; *) exit 1;; esac", nil
	case model.InitOpenRC:
		membership := "printf '%s\\n' \"$runlevels\" | awk -v name=" + name + " '$1 == name { found=1 } END { exit !found }'"
		if action == "start" {
			return "rc-update add " + name + " default && rc-service " + name + " start || exit 1\n" +
				"runlevels=$(rc-update show default) || exit 1\n" + membership + " && rc-service " + name + " status", nil
		}
		return "runlevels=$(rc-update show default) || exit 1\n" +
			"if " + membership + "; then rc-update del " + name + " default || exit 1; fi\n" +
			"rc-service " + name + " status >/dev/null 2>&1; state=$?\n" +
			"case \"$state\" in 0) rc-service " + name + " stop || exit 1;; 3) ;; *) exit 1;; esac\n" +
			"rc-service " + name + " status >/dev/null 2>&1; [ $? -eq 3 ] || exit 1\n" +
			"runlevels=$(rc-update show default) || exit 1\n" +
			"if " + membership + "; then exit 1; fi", nil
	default:
		return "", fmt.Errorf("unsupported init system %q", init)
	}
}
