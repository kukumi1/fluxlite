package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

// panelKeyPrefix starts the comment of every key the panel issues.
const panelKeyPrefix = "fluxlite-"

// pruneStaleKeysScript drops authorized_keys lines that carry the same comment
// as the key the panel just logged in with but a different key.
//
// Every re-enrollment issues a fresh key under the node's comment and the
// enrollment script only ever appends, so each reinstall left the previous
// key behind. Their private halves survive in old database copies, which makes
// every one of them a root login nobody manages.
//
// Matching on the exact comment, never on the fluxlite- prefix alone, keeps
// this from touching a key another node record uses on the same account. The
// rewrite only lands if the key in use survives it, so a bad match cannot lock
// the panel out. cat into the original keeps its owner, mode and SELinux label.
const pruneStaleKeysScript = `f="$HOME/.ssh/authorized_keys"; t="$f.fluxlite-prune"
[ -f "$f" ] || { echo 0; exit 0; }
awk '{ c = ""; for (i = 3; i <= NF; i++) c = c (i > 3 ? " " : "") $i }
     !(c == ENVIRON["FL_NOTE"] && $2 != ENVIRON["FL_BLOB"])' "$f" > "$t" || { rm -f "$t"; exit 1; }
removed=$(( $(awk 'END { print NR }' "$f") - $(awk 'END { print NR }' "$t") ))
if [ "$removed" -gt 0 ] && awk '$2 == ENVIRON["FL_BLOB"] { k = 1 } END { exit !k }' "$t"; then
    cat "$t" > "$f"
else
    removed=0
fi
rm -f "$t"
echo "$removed"`

// pruneStaleKeys removes keys a previous enrollment of this node left behind.
// It reports how many lines it removed.
func (s *Service) pruneStaleKeys(ctx context.Context, node *model.Node, client *ssh.Client) (int, error) {
	if node.AuthType != model.AuthKey {
		return 0, nil
	}
	secret, err := s.sealer.Open(node.AuthSecret)
	if err != nil {
		return 0, fmt.Errorf("open node key: %w", err)
	}
	id, err := cryptox.IdentityOf(secret)
	if errors.Is(err, cryptox.ErrNoKeyComment) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// A key the operator brought in has their own comment, and lines sharing
	// it are theirs to manage.
	if !strings.HasPrefix(id.Comment, panelKeyPrefix) {
		return 0, nil
	}

	cmd := "export FL_BLOB=" + sshx.Quote(id.Blob) + " FL_NOTE=" + sshx.Quote(id.Comment) + "\n" + pruneStaleKeysScript
	out, err := sshx.RunCheck(ctx, client, cmd)
	if err != nil {
		return 0, fmt.Errorf("prune stale keys: %w", err)
	}
	removed, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("prune stale keys: unexpected output %q", out)
	}
	return removed, nil
}

// sweepStaleKeys runs pruneStaleKeys on behalf of a probe, which must not fail
// because of it, and records what it removed.
func (s *Service) sweepStaleKeys(ctx context.Context, node *model.Node, client *ssh.Client) {
	removed, err := s.pruneStaleKeys(ctx, node, client)
	if err != nil {
		s.log.Warn("could not prune stale panel keys", "node", node.Name, "error", err)
		return
	}
	if removed == 0 {
		return
	}
	s.log.Warn("removed stale panel keys", "node", node.Name, "count", removed)
	entry := &model.AuditLog{
		Actor: "system", Action: "node.stale_keys_removed", Target: node.Name,
		Detail: fmt.Sprintf("移除了 %d 把旧的面板登录公钥（重装前留下的）", removed),
	}
	if err := s.store.AppendAudit(ctx, entry); err != nil {
		s.log.Error("audit stale key removal", "node", node.Name, "error", err)
	}
}
