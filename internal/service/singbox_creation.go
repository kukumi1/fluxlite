package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
)

// Narrow operation boundary permits deterministic failure tests without SSH.
type singBoxCreateOps struct {
	ports   func(context.Context, *model.Node) (map[int]bool, error)
	deploy  func(context.Context, *model.Node, *model.SingBoxUser, *singbox.Bundle) error
	cleanup func(context.Context, *model.Node, *model.SingBoxUser) error
}

// Remote errors can contain config excerpts. Preserve causal inspection without
// returning those excerpts (or generated credentials) to API callers.
type singBoxOperationError struct {
	message string
	cause   error
}

func (e *singBoxOperationError) Error() string { return e.message }
func (e *singBoxOperationError) Unwrap() error { return e.cause }

func rollbackSingBoxCreation(ctx context.Context, cause error, remoteTouched bool,
	cleanup, remove, retain func(context.Context) error) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	result := error(&singBoxOperationError{"create sing-box user failed", cause})
	if remoteTouched {
		if err := cleanup(rollbackCtx); err != nil {
			result = errors.Join(result, &singBoxOperationError{"remote cleanup incomplete; disabled user and port reservation retained", err})
			if err := retain(rollbackCtx); err != nil {
				result = errors.Join(result, &singBoxOperationError{"persist failed deployment state failed", err})
			}
			return result
		}
	}
	if err := remove(rollbackCtx); err != nil {
		result = errors.Join(result, &singBoxOperationError{"release sing-box reservation failed", err})
		if err := retain(rollbackCtx); err != nil {
			result = errors.Join(result, &singBoxOperationError{"persist failed deployment state failed", err})
		}
	}
	return result
}

func selectSingBoxPort(ctx context.Context, node *model.Node, requested int, used map[int]bool,
	claimed func(context.Context, int) (bool, error)) (int, error) {
	available := func(port int) (bool, error) {
		if port == node.SSHPort || used[port] {
			return false, nil
		}
		reserved, err := claimed(ctx, port)
		return !reserved, err
	}
	if requested != 0 {
		if requested < 1 || requested > 65535 {
			return 0, fmt.Errorf("invalid sing-box port")
		}
		free, err := available(requested)
		if err != nil {
			return 0, err
		}
		if !free {
			return 0, fmt.Errorf("port %d is already claimed on %s", requested, node.Name)
		}
		return requested, nil
	}
	for port := node.PortStart; port <= node.PortEnd && port <= 65535; port++ {
		if port < 1 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		free, err := available(port)
		if err != nil {
			return 0, err
		}
		if free {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port on node %s", node.Name)
}

// Removal is fail-closed: confirm the dedicated unit is stopped, remove its
// files and only its accounting rules. Never touch the shared sing-box service.
func singBoxRemovalCommand(init model.InitSystem, u *model.SingBoxUser) (string, error) {
	expected := fmt.Sprintf("fluxlite-singbox-%d", u.ID)
	if u.Source != model.SingBoxManaged || u.ID <= 0 || u.ServiceName != expected {
		return "", errors.New("refusing cleanup outside managed sing-box namespace")
	}
	unit := shellQuote(expected + ".service")
	var cmd string
	switch init {
	case model.InitSystemd:
		path := shellQuote("/etc/systemd/system/" + expected + ".service")
		cmd = "systemctl daemon-reload || exit 1\n" +
			"state=$(systemctl show " + unit + " --property=LoadState --value) || exit 1\n" +
			"if [ \"$state\" != not-found ]; then systemctl stop " + unit + " && systemctl disable " + unit + " || exit 1; fi\n" +
			"active=$(systemctl show " + unit + " --property=ActiveState --value) || exit 1\n" +
			"case \"$active\" in inactive|failed) ;; *) exit 1;; esac\n" +
			"rm -f " + path + " && systemctl daemon-reload || exit 1\n"
	case model.InitOpenRC:
		path := shellQuote("/etc/init.d/" + expected)
		name := shellQuote(expected)
		cmd = "if [ -e " + path + " ]; then\n" +
			"rc-service " + name + " status >/dev/null 2>&1; state=$?\n" +
			"case \"$state\" in 0) rc-service " + name + " stop || exit 1;; 3) ;; *) exit 1;; esac\n" +
			"rc-service " + name + " status >/dev/null 2>&1; [ $? -eq 3 ] || exit 1\n" +
			"runlevels=$(rc-update show default) || exit 1\n" +
			"if printf '%s\\n' \"$runlevels\" | awk -v name=" + name + " '$1 == name { found=1 } END { exit !found }'; then rc-update del " + name + " default || exit 1; fi\n" +
			"rm -f " + path + " || exit 1\nfi\n"
	default:
		return "", fmt.Errorf("unsupported init system %q", init)
	}
	cmd += fmt.Sprintf("rm -rf %s %s || exit 1\n", shellQuote(fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID)), shellQuote(fmt.Sprintf("/var/lib/fluxlite-singbox/%d", u.ID)))
	// Avoid a pipeline subshell swallowing deletion failures. Read the complete
	// chain first, and fail on listing/deletion errors rather than free the port.
	cmd += fmt.Sprintf(`for bin in iptables ip6tables; do
 if command -v "$bin" >/dev/null 2>&1; then
  chains=$("$bin" -S) || exit 1
  if printf '%%s\n' "$chains" | grep -q '^-N FLUXLITE_SB_ACCT$'; then
   rules=$("$bin" -S FLUXLITE_SB_ACCT) || exit 1
   deletes=$(printf '%%s\n' "$rules" | grep 'fluxlite-sb:%d:' | sed 's/^-A /-D /')
   while IFS= read -r rule; do
    [ -z "$rule" ] && continue
    eval "\"$bin\" $rule" || exit 1
   done <<EOF
$deletes
EOF
   remaining=$("$bin" -S FLUXLITE_SB_ACCT) || exit 1
   if printf '%%s\n' "$remaining" | grep -q 'fluxlite-sb:%d:'; then exit 1; fi
  fi
 fi
done`, u.ID, u.ID)
	return cmd, nil
}
