package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

type singBoxRemoteRun func(context.Context, string) (string, error)

func (s *Service) lockSingBoxAdoption(ctx context.Context) (func(), error) {
	s.singBoxAdoptionOnce.Do(func() { s.singBoxAdoptionGate = make(chan struct{}, 1) })
	select {
	case s.singBoxAdoptionGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.singBoxAdoptionGate
			return nil, err
		}
		return func() { <-s.singBoxAdoptionGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// A handover never changes the shared service's boot enablement or mask. A
// stopped shared service must stay stopped even when other fragments remain.
func sharedSingBoxState(ctx context.Context, initSystem model.InitSystem, run singBoxRemoteRun) (bool, string, string, error) {
	switch initSystem {
	case model.InitSystemd:
		out, err := run(ctx, "systemctl show sing-box.service --property=LoadState --property=ActiveState --property=UnitFileState")
		if err != nil {
			return false, "", "", err
		}
		state := make(map[string]string)
		for _, line := range strings.Split(out, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok {
				state[key] = value
			}
		}
		running := state["ActiveState"] == "active"
		if running && (state["LoadState"] == "masked" || strings.HasPrefix(state["UnitFileState"], "masked")) {
			return false, "", "", errors.New("shared sing-box is running but masked; cannot restore it without changing its mask")
		}
		if state["LoadState"] != "loaded" && state["LoadState"] != "masked" && state["LoadState"] != "not-found" {
			return false, "", "", fmt.Errorf("cannot inspect shared sing-box load state %q", state["LoadState"])
		}
		if !running && state["ActiveState"] != "inactive" && state["ActiveState"] != "failed" {
			return false, "", "", fmt.Errorf("shared sing-box is in transitional state %q; retry after it settles", state["ActiveState"])
		}
		return running, "systemctl stop sing-box.service", "systemctl start sing-box.service && systemctl is-active --quiet sing-box.service", nil
	case model.InitOpenRC:
		out, err := run(ctx, `rc-service --exists sing-box || exit 1
rc-service sing-box status >/dev/null 2>&1
status=$?
case "$status" in 0) echo active;; 3) echo inactive;; *) exit "$status";; esac`)
		if err != nil {
			return false, "", "", err
		}
		state := strings.TrimSpace(out)
		if state != "active" && state != "inactive" {
			return false, "", "", fmt.Errorf("cannot inspect shared sing-box OpenRC state %q", state)
		}
		return state == "active", "rc-service sing-box stop", "rc-service sing-box start", nil
	default:
		return false, "", "", fmt.Errorf("unsupported init system %q", initSystem)
	}
}

// Callbacks keep SSH and storage at the boundary so all failure paths can be
// exercised without a real node. The database remains external until success.
func handoverSingBox(ctx context.Context, initSystem model.InitSystem, path, backup string, run singBoxRemoteRun,
	deploy, cleanup, commit func(context.Context) error) error {
	running, stop, start, err := sharedSingBoxState(ctx, initSystem, run)
	if err != nil {
		return fmt.Errorf("inspect shared sing-box before handover: %w", err)
	}
	disabled := path + ".disabled"
	// Do not overwrite a pre-existing disabled fragment during a retry.
	if _, err := run(ctx, "test ! -e "+shellQuote(disabled)+" && test ! -L "+shellQuote(disabled)+" && cp "+shellQuote(path)+" "+shellQuote(backup)); err != nil {
		return fmt.Errorf("back up external sing-box fragment: %w", err)
	}
	deployed := false
	rollback := func(cause error) error {
		// An HTTP cancellation must not strand the node with both services stopped.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if deployed {
			if running {
				if _, err := run(rollbackCtx, stop); err != nil {
					return errors.Join(cause, fmt.Errorf("stop shared sing-box for rollback: %w", err))
				}
			}
			if err := cleanup(rollbackCtx); err != nil {
				// Do not start the original service while the managed one may still
				// own its port. Report the incomplete rollback rather than hide it.
				return errors.Join(cause, fmt.Errorf("roll back managed sing-box service: %w", err))
			}
		}
		if _, err := run(rollbackCtx, "cp "+shellQuote(backup)+" "+shellQuote(path)+" && rm -f "+shellQuote(disabled)); err != nil {
			return errors.Join(cause, fmt.Errorf("restore external sing-box fragment: %w", err))
		}
		if running {
			if _, err := run(rollbackCtx, start); err != nil {
				return errors.Join(cause, fmt.Errorf("restore shared sing-box running state: %w", err))
			}
		}
		return cause
	}
	if running {
		if _, err := run(ctx, stop); err != nil {
			return rollback(fmt.Errorf("stop shared sing-box for handover: %w", err))
		}
	}
	deployed = true // Deployment may fail after creating or starting the unit.
	if err := deploy(ctx); err != nil {
		return rollback(fmt.Errorf("deploy adopted sing-box service: %w", err))
	}
	if _, err := run(ctx, "mv "+shellQuote(path)+" "+shellQuote(disabled)); err != nil {
		return rollback(fmt.Errorf("disable external sing-box fragment: %w", err))
	}
	if running {
		if _, err := run(ctx, start); err != nil {
			return rollback(fmt.Errorf("finish shared sing-box handover: %w", err))
		}
	}
	if err := commit(ctx); err != nil {
		return rollback(fmt.Errorf("save adopted sing-box user: %w", err))
	}
	return nil
}

// Remove a partially deployed adoption only after confirming its service has
// stopped. Unlike best-effort deletion, handover rollback must surface failures.
func (s *Service) rollbackAdoptedSingBoxUser(ctx context.Context, node *model.Node, u *model.SingBoxUser) error {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	var cmd string
	if node.InitSystem == model.InitSystemd {
		unitPath := "/etc/systemd/system/" + u.ServiceName + ".service"
		cmd = "if [ -e " + shellQuote(unitPath) + " ] || [ -L " + shellQuote(unitPath) + " ]; then systemctl daemon-reload && systemctl stop " + shellQuote(u.ServiceName+".service") + " && systemctl disable " + shellQuote(u.ServiceName+".service") + " && rm -f " + shellQuote(unitPath) + " && systemctl daemon-reload || exit 1; fi"
	} else if node.InitSystem == model.InitOpenRC {
		path := "/etc/init.d/" + u.ServiceName
		// A config validation failure can leave the script written but not yet
		// registered in the default runlevel. Delete membership only if present.
		cmd = "if [ -e " + shellQuote(path) + " ]; then\n" +
			"rc-service " + shellQuote(u.ServiceName) + " stop || exit 1\n" +
			"runlevels=$(rc-update show default) || exit 1\n" +
			"if printf '%s\\n' \"$runlevels\" | awk -v name=" + shellQuote(u.ServiceName) + " '$1 == name { found=1 } END { exit !found }'; then rc-update del " + shellQuote(u.ServiceName) + " default || exit 1; fi\n" +
			"rm -f " + shellQuote(path) + " || exit 1\nfi"
	} else {
		return fmt.Errorf("unsupported init system %q", node.InitSystem)
	}
	if _, err := sshx.RunCheck(ctx, client.Client, cmd); err != nil {
		return err
	}
	// Check directory removal separately: ordinary deletion is best-effort, but
	// adoption rollback must not claim success if these writes fail.
	cmd = "rm -rf " + shellQuote(fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID)) + " " + shellQuote(fmt.Sprintf("/var/lib/fluxlite-singbox/%d", u.ID))
	if _, err := sshx.RunCheck(ctx, client.Client, cmd); err != nil {
		return err
	}
	// The service and config are gone; clear existing accounting rules too.
	return s.removeSingBoxUser(ctx, node, u)
}
