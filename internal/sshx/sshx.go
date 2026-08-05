// Package sshx dials managed nodes, transparently chaining through jump hosts
// for nodes that are not directly reachable from the controller.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
)

// maxJumpDepth bounds how many hops a via chain may have. It also stops a
// mis-configured cycle from recursing forever.
const maxJumpDepth = 8

var (
	ErrJumpChainTooDeep = errors.New("jump chain exceeds maximum depth")
	ErrJumpCycle        = errors.New("jump chain contains a cycle")
	ErrHostKeyMismatch  = errors.New("host key mismatch")
)

// NodeSource resolves node records by ID so the dialer can walk a via chain
// without depending on the storage layer.
type NodeSource interface {
	NodeByID(ctx context.Context, id int64) (*model.Node, error)
}

// SecretOpener decrypts a node's stored credential.
type SecretOpener interface {
	Open(sealed []byte) ([]byte, error)
}

// HostKeyRecorder persists a host key learned on first connect.
type HostKeyRecorder interface {
	SetNodeHostKey(ctx context.Context, nodeID int64, hostKey string) error
}

// Dialer builds authenticated SSH clients for managed nodes.
type Dialer struct {
	nodes    NodeSource
	secrets  SecretOpener
	hostKeys HostKeyRecorder
	timeout  time.Duration
}

func NewDialer(nodes NodeSource, secrets SecretOpener, hostKeys HostKeyRecorder, timeout time.Duration) *Dialer {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Dialer{nodes: nodes, secrets: secrets, hostKeys: hostKeys, timeout: timeout}
}

// Client is an SSH session to a node. Closing it also tears down any jump
// clients opened on its behalf.
type Client struct {
	*ssh.Client
	parents []*ssh.Client
}

// Close releases the client and its jump chain, innermost first.
func (c *Client) Close() error {
	err := c.Client.Close()
	for i := len(c.parents) - 1; i >= 0; i-- {
		if cerr := c.parents[i].Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// Dial connects to node, chaining through its via nodes when present.
func (d *Dialer) Dial(ctx context.Context, node *model.Node) (*Client, error) {
	chain, err := d.resolveChain(ctx, node)
	if err != nil {
		return nil, err
	}

	var parents []*ssh.Client
	closeParents := func() {
		for i := len(parents) - 1; i >= 0; i-- {
			parents[i].Close()
		}
	}

	// chain is ordered outermost jump host first, target last.
	var current *ssh.Client
	for i, hop := range chain {
		client, err := d.dialOne(ctx, hop, current)
		if err != nil {
			closeParents()
			return nil, fmt.Errorf("dial %s: %w", hop.Name, err)
		}
		if i < len(chain)-1 {
			parents = append(parents, client)
		}
		current = client
	}
	return &Client{Client: current, parents: parents}, nil
}

// resolveChain walks via pointers and returns the hops to dial in order,
// ending with node itself.
func (d *Dialer) resolveChain(ctx context.Context, node *model.Node) ([]*model.Node, error) {
	chain := []*model.Node{node}
	seen := map[int64]bool{node.ID: true}

	current := node
	for current.ViaNodeID != nil {
		if len(chain) >= maxJumpDepth {
			return nil, ErrJumpChainTooDeep
		}
		via, err := d.nodes.NodeByID(ctx, *current.ViaNodeID)
		if err != nil {
			return nil, fmt.Errorf("resolve jump host %d: %w", *current.ViaNodeID, err)
		}
		if seen[via.ID] {
			return nil, ErrJumpCycle
		}
		seen[via.ID] = true
		chain = append(chain, via)
		current = via
	}

	// Reverse so the outermost jump host is dialed first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// dialOne establishes one SSH connection, tunnelling through via when set.
func (d *Dialer) dialOne(ctx context.Context, node *model.Node, via *ssh.Client) (*ssh.Client, error) {
	cfg, err := d.clientConfig(ctx, node)
	if err != nil {
		return nil, err
	}
	addr := node.Addr()

	var conn net.Conn
	if via == nil {
		dialer := &net.Dialer{Timeout: d.timeout}
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	} else {
		// ssh.Client.Dial has no context variant; the handshake deadline below
		// bounds it instead.
		conn, err = via.Dial("tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("tcp connect: %w", err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(d.timeout))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	// Clear the handshake deadline so long-running sessions are not killed.
	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), nil
}

func (d *Dialer) clientConfig(ctx context.Context, node *model.Node) (*ssh.ClientConfig, error) {
	secret, err := d.secrets.Open(node.AuthSecret)
	if err != nil {
		return nil, fmt.Errorf("open credential: %w", err)
	}

	var auth ssh.AuthMethod
	switch node.AuthType {
	case model.AuthKey:
		signer, err := ssh.ParsePrivateKey(secret)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	case model.AuthPassword:
		auth = ssh.Password(string(secret))
	default:
		return nil, model.ErrNodeAuthType
	}

	return &ssh.ClientConfig{
		User:            node.SSHUser,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: d.hostKeyCallback(ctx, node),
		Timeout:         d.timeout,
	}, nil
}

// hostKeyCallback pins the node's host key. The first successful connect
// records the key (trust on first use); every later connect must match it.
func (d *Dialer) hostKeyCallback(ctx context.Context, node *model.Node) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		presented := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		known := strings.TrimSpace(node.HostKey)

		if known == "" {
			if d.hostKeys != nil {
				if err := d.hostKeys.SetNodeHostKey(ctx, node.ID, presented); err != nil {
					return fmt.Errorf("record host key: %w", err)
				}
			}
			node.HostKey = presented
			return nil
		}
		if known != presented {
			return fmt.Errorf("%w for %s: stored %.32s..., got %.32s...",
				ErrHostKeyMismatch, node.Name, known, presented)
		}
		return nil
	}
}

// Result holds the outcome of a remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes a command and returns its output. A non-zero exit is reported
// in Result.ExitCode rather than as an error, so callers can distinguish a
// failed command from a broken connection.
func Run(ctx context.Context, client *ssh.Client, cmd string) (*Result, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return nil, ctx.Err()
	case err := <-done:
		res := &Result{Stdout: stdout.String(), Stderr: stderr.String()}
		if err == nil {
			return res, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		return nil, fmt.Errorf("run %q: %w", cmd, err)
	}
}

// RunCheck executes a command and fails if it exits non-zero.
func RunCheck(ctx context.Context, client *ssh.Client, cmd string) (string, error) {
	res, err := Run(ctx, client, cmd)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("command %q exited %d: %s",
			cmd, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// WriteFile writes content to path on the remote host with the given mode.
// It streams through `cat` rather than SFTP so nodes without an SFTP
// subsystem (busybox, hardened images) still work.
func WriteFile(ctx context.Context, client *ssh.Client, path string, content []byte, mode string) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	// The temp-file-then-rename dance makes the replacement atomic, so a
	// reader never observes a half-written config.
	tmp := path + ".fluxlite.tmp"
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %s %s && mv -f %s %s",
		shellQuote(dirOf(path)), shellQuote(tmp), mode, shellQuote(tmp), shellQuote(tmp), shellQuote(path))

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("start write: %w", err)
	}
	if _, err := io.Copy(stdin, bytes.NewReader(content)); err != nil {
		stdin.Close()
		return fmt.Errorf("stream content: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("close stdin: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("write %s: %w (%s)", path, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "/"
}

// shellQuote wraps a value in single quotes for safe interpolation into a
// remote shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Quote exposes shell quoting to packages that build remote commands.
func Quote(s string) string { return shellQuote(s) }

// Pool caches dialed clients so a batch of operations against one node does
// not pay the handshake cost repeatedly.
type Pool struct {
	dialer *Dialer

	mu      sync.Mutex
	clients map[int64]*Client
}

func NewPool(dialer *Dialer) *Pool {
	return &Pool{dialer: dialer, clients: make(map[int64]*Client)}
}

// Get returns a cached client for the node, dialing one if needed.
func (p *Pool) Get(ctx context.Context, node *model.Node) (*Client, error) {
	p.mu.Lock()
	if c, ok := p.clients[node.ID]; ok {
		p.mu.Unlock()
		if alive(c) {
			return c, nil
		}
		// The node rebooted or the link dropped. Handing the dead client back
		// would fail every operation on this node until the panel restarts.
		p.mu.Lock()
		if current, ok := p.clients[node.ID]; ok && current == c {
			delete(p.clients, node.ID)
			c.Close()
		}
		p.mu.Unlock()
	} else {
		p.mu.Unlock()
	}

	client, err := p.dialer.Dial(ctx, node)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// Another goroutine may have populated the entry while we dialed.
	if existing, ok := p.clients[node.ID]; ok {
		client.Close()
		return existing, nil
	}
	p.clients[node.ID] = client
	return client, nil
}

// alive reports whether a cached connection still answers. A keepalive request
// costs one round trip, far less than the handshake a false negative would
// force, and unlike opening a session it leaves no state behind on the node.
func alive(c *Client) bool {
	_, _, err := c.Client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}

// Close tears down every cached client.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		c.Close()
		delete(p.clients, id)
	}
}
