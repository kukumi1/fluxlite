package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestSharedSingBoxState(t *testing.T) {
	cases := []struct {
		name, load, active, enabled string
		running, rejected           bool
	}{
		{"active enabled", "loaded", "active", "enabled", true, false},
		{"active disabled", "loaded", "active", "disabled", true, false},
		{"inactive enabled", "loaded", "inactive", "enabled", false, false},
		{"inactive disabled", "loaded", "inactive", "disabled", false, false},
		{"masked", "masked", "inactive", "masked", false, false},
		{"runtime masked", "masked", "inactive", "masked-runtime", false, false},
		{"failed", "loaded", "failed", "enabled", false, false},
		{"missing", "not-found", "inactive", "", false, false},
		{"active masked", "masked", "active", "masked", false, true},
		{"activating", "loaded", "activating", "enabled", false, true},
		{"deactivating", "loaded", "deactivating", "enabled", false, true},
		{"unreadable", "", "", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(_ context.Context, cmd string) (string, error) {
				if cmd != "systemctl show sing-box.service --property=LoadState --property=ActiveState --property=UnitFileState" {
					t.Fatalf("state inspection mutated service: %s", cmd)
				}
				return "LoadState=" + tc.load + "\nActiveState=" + tc.active + "\nUnitFileState=" + tc.enabled + "\n", nil
			}
			running, _, _, err := sharedSingBoxState(context.Background(), model.InitSystemd, run)
			if running != tc.running || (err != nil) != tc.rejected {
				t.Fatalf("got running=%v error=%v; want running=%v rejected=%v", running, err, tc.running, tc.rejected)
			}
		})
	}
}

func TestSharedSingBoxStateOpenRC(t *testing.T) {
	for _, state := range []string{"active", "inactive", "unknown"} {
		t.Run(state, func(t *testing.T) {
			running, stop, start, err := sharedSingBoxState(context.Background(), model.InitOpenRC,
				func(_ context.Context, cmd string) (string, error) {
					if !strings.Contains(cmd, "rc-service --exists sing-box") || strings.Contains(cmd, "systemctl") {
						t.Fatalf("wrong OpenRC state command: %s", cmd)
					}
					return state, nil
				})
			if state == "unknown" {
				if err == nil {
					t.Fatal("unknown state must be rejected")
				}
				return
			}
			if err != nil || running != (state == "active") || stop != "rc-service sing-box stop" || start != "rc-service sing-box start" {
				t.Fatalf("unexpected state/commands: %v %q %q %v", running, stop, start, err)
			}
		})
	}
}

func TestSingBoxHandover(t *testing.T) {
	const path = "/etc/sing-box/conf.d/CDT-HK.json"
	const backup = path + ".fluxlite-backup-9"
	remoteFailure := errors.New("remote failed")
	cases := []struct {
		name, load, active, enabled, failure string
		events                               []string
	}{
		{"active", "loaded", "active", "enabled", "", []string{"inspect", "backup", "stop", "deploy", "disable-fragment", "start", "commit"}},
		{"active boot disabled", "loaded", "active", "disabled", "", []string{"inspect", "backup", "stop", "deploy", "disable-fragment", "start", "commit"}},
		{"inactive", "loaded", "inactive", "enabled", "", []string{"inspect", "backup", "deploy", "disable-fragment", "commit"}},
		{"masked", "masked", "inactive", "masked", "", []string{"inspect", "backup", "deploy", "disable-fragment", "commit"}},
		{"disabled", "loaded", "inactive", "disabled", "", []string{"inspect", "backup", "deploy", "disable-fragment", "commit"}},
		{"state read fails", "loaded", "active", "enabled", "inspect", []string{"inspect"}},
		{"backup fails", "loaded", "active", "enabled", "backup", []string{"inspect", "backup"}},
		{"stop fails", "loaded", "active", "enabled", "stop", []string{"inspect", "backup", "stop", "restore-fragment", "start"}},
		{"deploy fails", "loaded", "active", "enabled", "deploy", []string{"inspect", "backup", "stop", "deploy", "stop", "cleanup", "restore-fragment", "start"}},
		{"masked deploy fails", "masked", "inactive", "masked", "deploy", []string{"inspect", "backup", "deploy", "cleanup", "restore-fragment"}},
		{"rename fails", "loaded", "active", "enabled", "disable-fragment", []string{"inspect", "backup", "stop", "deploy", "disable-fragment", "stop", "cleanup", "restore-fragment", "start"}},
		{"shared start fails", "loaded", "active", "enabled", "start", []string{"inspect", "backup", "stop", "deploy", "disable-fragment", "start", "stop", "cleanup", "restore-fragment", "start"}},
		{"database commit fails", "loaded", "active", "enabled", "commit", []string{"inspect", "backup", "stop", "deploy", "disable-fragment", "start", "commit", "stop", "cleanup", "restore-fragment", "start"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			sharedRunning := tc.active == "active"
			originalPresent, disabledPresent, managedRunning, committed := true, false, false, false
			failed := false
			step := func(event string) error {
				events = append(events, event)
				if tc.failure == event && !failed {
					failed = true
					return remoteFailure
				}
				return nil
			}
			run := func(_ context.Context, cmd string) (string, error) {
				if strings.Contains(cmd, "unmask") || strings.Contains(cmd, " enable ") || strings.Contains(cmd, " disable ") {
					t.Fatalf("shared mask/boot enablement must not change: %s", cmd)
				}
				switch {
				case strings.HasPrefix(cmd, "systemctl show "):
					return "LoadState=" + tc.load + "\nActiveState=" + tc.active + "\nUnitFileState=" + tc.enabled, step("inspect")
				case strings.HasPrefix(cmd, "test ! -e "):
					if !strings.Contains(cmd, "test ! -L ") || !strings.Contains(cmd, "cp "+shellQuote(path)+" "+shellQuote(backup)) {
						t.Fatalf("backup must protect existing disabled files: %s", cmd)
					}
					return "", step("backup")
				case cmd == "systemctl stop sing-box.service":
					err := step("stop")
					sharedRunning = false // An unsuccessful command may have changed state.
					return "", err
				case cmd == "systemctl start sing-box.service && systemctl is-active --quiet sing-box.service":
					if tc.load == "masked" {
						t.Fatal("masked shared service must never be started")
					}
					err := step("start")
					sharedRunning = err == nil
					return "", err
				case cmd == "mv "+shellQuote(path)+" "+shellQuote(path+".disabled"):
					if err := step("disable-fragment"); err != nil {
						return "", err
					}
					originalPresent, disabledPresent = false, true
					return "", nil
				case cmd == "cp "+shellQuote(backup)+" "+shellQuote(path)+" && rm -f "+shellQuote(path+".disabled"):
					if managedRunning || (tc.active == "active" && sharedRunning) {
						t.Fatal("restore must follow service shutdown to avoid conflicting active configuration")
					}
					originalPresent, disabledPresent = true, false
					return "", step("restore-fragment")
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return "", nil
				}
			}
			err := handoverSingBox(context.Background(), model.InitSystemd, path, backup, run,
				func(context.Context) error {
					managedRunning = true // Include partially successful deployment.
					return step("deploy")
				},
				func(context.Context) error {
					managedRunning = false
					return step("cleanup")
				},
				func(context.Context) error {
					if err := step("commit"); err != nil {
						return err
					}
					committed = true
					return nil
				})
			if !reflect.DeepEqual(events, tc.events) {
				t.Fatalf("events = %v; want %v", events, tc.events)
			}
			if tc.failure == "" {
				if err != nil || !committed || originalPresent || !disabledPresent || !managedRunning {
					t.Fatalf("handover incomplete: err=%v committed=%v original=%v disabled=%v managed=%v", err, committed, originalPresent, disabledPresent, managedRunning)
				}
			} else if !errors.Is(err, remoteFailure) || committed || !originalPresent || disabledPresent || managedRunning {
				t.Fatalf("rollback incomplete: err=%v committed=%v original=%v disabled=%v managed=%v", err, committed, originalPresent, disabledPresent, managedRunning)
			}
			if sharedRunning != (tc.active == "active") {
				t.Fatalf("shared running state changed: %v", sharedRunning)
			}
		})
	}
}

func TestSingBoxHandoverFailedSharedRestoreIsReported(t *testing.T) {
	deployError, restoreError := errors.New("deploy failed"), errors.New("shared restart failed")
	run := func(_ context.Context, cmd string) (string, error) {
		if strings.HasPrefix(cmd, "systemctl show ") {
			return "LoadState=loaded\nActiveState=active\nUnitFileState=disabled", nil
		}
		if strings.HasPrefix(cmd, "systemctl start ") {
			return "", restoreError
		}
		return "", nil
	}
	err := handoverSingBox(context.Background(), model.InitSystemd, "/fragment.json", "/backup", run,
		func(context.Context) error { return deployError },
		func(context.Context) error { return nil },
		func(context.Context) error { t.Fatal("failed adoption must not commit"); return nil })
	if !errors.Is(err, deployError) || !errors.Is(err, restoreError) {
		t.Fatalf("failed shared restart must not be ignored: %v", err)
	}
}

func TestSingBoxHandoverOpenRC(t *testing.T) {
	for _, state := range []string{"active", "inactive"} {
		t.Run(state, func(t *testing.T) {
			var services []string
			run := func(_ context.Context, cmd string) (string, error) {
				if strings.Contains(cmd, "systemctl") || strings.Contains(cmd, "rc-update") {
					t.Fatalf("wrong init or boot enablement change: %s", cmd)
				}
				if strings.HasPrefix(cmd, "rc-service --exists ") {
					return state, nil
				}
				if strings.HasPrefix(cmd, "rc-service sing-box ") {
					services = append(services, cmd)
				}
				return "", nil
			}
			err := handoverSingBox(context.Background(), model.InitOpenRC, "/fragment.json", "/backup", run,
				func(context.Context) error { return nil },
				func(context.Context) error { t.Fatal("successful handover must not roll back"); return nil },
				func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if state == "active" && !reflect.DeepEqual(services, []string{"rc-service sing-box stop", "rc-service sing-box start"}) {
				t.Fatalf("active shared service must restart: %v", services)
			}
			if state == "inactive" && len(services) != 0 {
				t.Fatalf("inactive shared service must stay untouched: %v", services)
			}
		})
	}
}

func TestSingBoxHandoverRollbackFailureIsReported(t *testing.T) {
	deployError, cleanupError := errors.New("deploy failed"), errors.New("managed stop failed")
	restored := false
	run := func(_ context.Context, cmd string) (string, error) {
		if strings.HasPrefix(cmd, "systemctl show ") {
			return "LoadState=masked\nActiveState=inactive\nUnitFileState=masked", nil
		}
		if strings.HasPrefix(cmd, "cp ") {
			restored = true
		}
		return "", nil
	}
	err := handoverSingBox(context.Background(), model.InitSystemd, "/fragment.json", "/backup", run,
		func(context.Context) error { return deployError },
		func(context.Context) error { return cleanupError },
		func(context.Context) error { t.Fatal("failed adoption must not commit"); return nil })
	if !errors.Is(err, deployError) || !errors.Is(err, cleanupError) || restored {
		t.Fatalf("must report both failures without restoring conflicting original: %v restored=%v", err, restored)
	}
}

func TestSingBoxHandoverRollbackSurvivesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	run := func(ctx context.Context, cmd string) (string, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("rollback inherited canceled request: %v", err)
		}
		if strings.HasPrefix(cmd, "systemctl show ") {
			return "LoadState=loaded\nActiveState=active\nUnitFileState=enabled", nil
		}
		return "", nil
	}
	err := handoverSingBox(ctx, model.InitSystemd, "/fragment.json", "/backup", run,
		func(context.Context) error { cancel(); return context.Canceled },
		func(ctx context.Context) error { return ctx.Err() },
		func(context.Context) error { t.Fatal("canceled adoption must not commit"); return nil })
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "roll back") {
		t.Fatalf("expected original cancellation with successful rollback: %v", err)
	}
}
