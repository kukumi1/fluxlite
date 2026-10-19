package service

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestSingBoxUnitCommandsValidateNamespace(t *testing.T) {
	for _, u := range []*model.SingBoxUser{nil, {ID: 1, Source: model.SingBoxExternal, ServiceName: "sing-box"}, {ID: 1, Source: model.SingBoxManaged, ServiceName: "sing-box"}, {ID: 0, Source: model.SingBoxManaged, ServiceName: "fluxlite-singbox-0"}} {
		if _, err := singBoxUnitCommand(model.InitSystemd, u, "stop"); err == nil {
			t.Fatal("accepted unmanaged unit")
		}
	}
	u := &model.SingBoxUser{ID: 1, Source: model.SingBoxManaged, ServiceName: "fluxlite-singbox-1"}
	if _, err := singBoxUnitCommand(model.InitSystemd, u, "restart; false"); err == nil {
		t.Fatal("accepted unsupported action")
	}
	if _, err := singBoxUnitCommand(model.InitSystem("unknown"), u, "stop"); err == nil {
		t.Fatal("accepted unsupported init system")
	}
}

func TestSingBoxAvailabilityBootPolicy(t *testing.T) {
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "C:/Program Files/Git/bin/bash.exe"
		if _, err := os.Stat(shell); err != nil {
			t.Skip("POSIX shell unavailable")
		}
	} else if _, err := exec.LookPath(shell); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	u := &model.SingBoxUser{ID: 1, Source: model.SingBoxManaged, ServiceName: "fluxlite-singbox-1"}
	for _, init := range []model.InitSystem{model.InitSystemd, model.InitOpenRC} {
		t.Run(string(init), func(t *testing.T) {
			start, err := singBoxUnitCommand(init, u, "start")
			if err != nil {
				t.Fatal(err)
			}
			stop, err := singBoxUnitCommand(init, u, "stop")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stop, "|| true") {
				t.Fatal("silently swallowed failure")
			}
			// Execute only shell functions, never a real service manager. Child shells
			// model remote command sessions; files hold mock boot and process state.
			mock := `
setstate() { printf '%s' "$2" > "$1"; }
getstate() { cat "$1"; }
systemctl() {
 case "$1" in
 enable) setstate boot enabled; setstate active active;;
 disable) setstate boot disabled; setstate active inactive;;
 show) case "$3" in --property=UnitFileState) getstate boot;; --property=ActiveState) getstate active;; *) return 1;; esac;;
 is-enabled) [ "$(getstate boot)" = enabled ];;
 is-active) [ "$(getstate active)" = active ];;
 *) return 1;; esac
}
rc-update() {
 case "$1" in
 add) setstate boot enabled;;
 del) setstate boot disabled;;
 show) if [ "$(getstate boot)" = enabled ]; then echo 'fluxlite-singbox-1 | default'; fi;;
 *) return 1;; esac
}
rc-service() {
 case "$2" in
 start) setstate active active;;
 stop) setstate active inactive;;
 status) [ "$(getstate active)" = active ] || return 3;;
 *) return 1;; esac
}
setstate boot enabled
setstate active active
`
			script := mock + "\n(\n" + stop + "\n) || exit 11\n(\n" + stop + "\n) || exit 12\n" +
				"[ \"$(getstate boot)\" = disabled ] && [ \"$(getstate active)\" = inactive ] || exit 13\n" +
				"(\n" + start + "\n) || exit 14\n(\n" + start + "\n) || exit 15\n" +
				"[ \"$(getstate boot)\" = enabled ] && [ \"$(getstate active)\" = active ] || exit 16\n"
			cmd := exec.Command(shell, "-c", script)
			cmd.Dir = t.TempDir()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("boot-policy simulation failed: %v: %s", err, out)
			}
			// A stop that reports success but leaves a process alive must fail verify.
			mock += "\nsystemctl() { case \"$1\" in disable) return 0;; show) case \"$3\" in --property=UnitFileState) echo disabled;; *) echo active;; esac;; *) return 1;; esac; }\n" +
				"rc-service() { case \"$2\" in stop|status) return 0;; *) return 1;; esac; }\n"
			cmd = exec.Command(shell, "-c", mock+"\n"+stop)
			cmd.Dir = t.TempDir()
			if _, err := cmd.CombinedOutput(); err == nil {
				t.Fatal("stop accepted a still-running service")
			}
		})
	}
}
