// Package scp implements an SSH/SCP-backed store.Target.
//
// The SCP wire protocol is inlined on top of golang.org/x/crypto/ssh; no
// third-party SCP client library is imported. Host keys are verified against
// the operator's ~/.ssh/known_hosts file; connecting to an unknown host is
// a hard error (no trust-on-first-use prompt).
package scp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// stderrCaptureLimit caps how many bytes of remote scp stderr are preserved
// for error-wrapping. Remote peers that spew megabytes of diagnostics would
// otherwise inflate the error chain; 4 KiB is enough to surface the first
// diagnostic line without unbounded memory growth.
const stderrCaptureLimit = 4 << 10

// Name is the Target identifier reported by SCPTarget.Name.
const Name = "scp"

// dialTimeout bounds the TCP + SSH handshake duration.
const dialTimeout = 30 * time.Second

// sessionTimeout bounds a single SCP upload session (handshake + transfer).
const sessionTimeout = 60 * time.Minute

// ackTimeout bounds a single readAck call so a dead peer cannot wedge the
// session indefinitely. The SCP ack exchange is expected to complete in
// milliseconds on healthy links; 30 seconds is generous enough for pathological
// WAN latency without masking a hung peer.
const ackTimeout = 30 * time.Second

// envKnownHosts names the environment variable that overrides known_hosts
// discovery. When set and pointing to a readable file, its value wins over
// the system and per-user defaults.
const envKnownHosts = "PEBBLIFY_SCP_KNOWN_HOSTS"

// systemKnownHosts is the operator-editable path consulted when the env
// override is unset; it lets package deployments ship a baked-in trust file
// without requiring a $HOME.
const systemKnownHosts = "/etc/pebblify/known_hosts"

// Sentinel errors returned by the SCP target.
var (
	// ErrUnsupportedAuth indicates cfg.AuthentificationMode is unrecognised.
	ErrUnsupportedAuth = errors.New("scp: unsupported authentification_mode")
	// ErrMissingSecret indicates a required secret (key path, password) is empty.
	ErrMissingSecret = errors.New("scp: missing required secret")
	// ErrKnownHosts indicates the known_hosts file could not be loaded or the
	// remote host is not present in it.
	ErrKnownHosts = errors.New("scp: known_hosts validation failed")
	// ErrProtocol indicates the remote peer returned an SCP protocol error.
	ErrProtocol = errors.New("scp: protocol error")
)

// SCPTarget uploads archives to a remote host using the OpenSSH scp protocol
// (sink mode, single-file copy). Remote paths are always absolute under the
// operator-supplied base directory.
type SCPTarget struct {
	host     string
	port     int
	username string
	authCfg  ssh.AuthMethod
	hostKey  ssh.HostKeyCallback
	remote   string
}

// New constructs an SCPTarget from the TOML section and the secrets bundle.
//
// The remote destination directory is cfg.Username's home-relative path as
// encoded in the config's SavePath? No — cfg does not carry a remote path,
// so uploads are placed in the account's default SFTP landing (the user's
// home). Callers needing a subdirectory encode it in remoteName.
func New(cfg config.SCPSaveSection, secrets config.Secrets) (*SCPTarget, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("%w: host must not be empty", ErrMissingSecret)
	}
	if cfg.Username == "" {
		return nil, fmt.Errorf("%w: username must not be empty", ErrMissingSecret)
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, fmt.Errorf("%w: port %d out of range", ErrMissingSecret, cfg.Port)
	}

	auth, err := buildAuth(cfg.AuthentificationMode, secrets)
	if err != nil {
		return nil, err
	}

	callback, err := loadHostKeyCallback()
	if err != nil {
		return nil, err
	}

	return &SCPTarget{
		host:     cfg.Host,
		port:     cfg.Port,
		username: cfg.Username,
		authCfg:  auth,
		hostKey:  callback,
		remote:   ".",
	}, nil
}

// Name implements store.Target.
func (t *SCPTarget) Name() string { return Name }

// Upload implements store.Target. It opens a single SSH session per call and
// pushes localPath to the remote working directory under remoteName.
func (t *SCPTarget) Upload(ctx context.Context, localPath, remoteName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if localPath == "" || remoteName == "" {
		return errors.New("scp upload: localPath and remoteName must be non-empty")
	}
	if err := validateRemoteName(remoteName); err != nil {
		return fmt.Errorf("scp upload: %w", err)
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("scp upload: stat %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("scp upload: %s is not a regular file", localPath)
	}

	sessionCtx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()

	cfg := &ssh.ClientConfig{
		User:            t.username,
		Auth:            []ssh.AuthMethod{t.authCfg},
		HostKeyCallback: t.hostKey,
		Timeout:         dialTimeout,
	}

	client, err := dialContext(sessionCtx, "tcp",
		net.JoinHostPort(t.host, strconv.Itoa(t.port)), cfg)
	if err != nil {
		return fmt.Errorf("scp upload: dial %s: %w", t.host, err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("scp upload: session: %w", err)
	}
	defer func() { _ = session.Close() }()

	return runSCPSink(sessionCtx, session, localPath, info, path.Join(t.remote, remoteName))
}

// buildAuth resolves the ssh.AuthMethod for the configured auth mode.
func buildAuth(mode string, secrets config.Secrets) (ssh.AuthMethod, error) {
	switch mode {
	case config.SCPAuthKey:
		if secrets.SCPKeyPath == "" {
			return nil, fmt.Errorf("%w: SCP key path", ErrMissingSecret)
		}
		pem, err := os.ReadFile(secrets.SCPKeyPath)
		if err != nil {
			return nil, fmt.Errorf("scp: read key %s: %w", secrets.SCPKeyPath, err)
		}
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			return nil, fmt.Errorf("scp: parse private key: %w", err)
		}
		return ssh.PublicKeys(signer), nil
	case config.SCPAuthPassword:
		if secrets.SCPPassword == "" {
			return nil, fmt.Errorf("%w: SCP password", ErrMissingSecret)
		}
		return ssh.Password(secrets.SCPPassword), nil
	case config.SCPAuthNone:
		return ssh.Password(""), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAuth, mode)
	}
}

// loadHostKeyCallback returns an ssh.HostKeyCallback that checks the
// known_hosts file resolved via the following precedence:
//
//  1. $PEBBLIFY_SCP_KNOWN_HOSTS if set and the referenced file is readable.
//  2. /etc/pebblify/known_hosts if it exists.
//  3. $HOME/.ssh/known_hosts as the per-user fallback.
//
// If none of the above resolve to a readable file, ErrKnownHosts is returned
// so the daemon refuses to construct an SCPTarget that would otherwise accept
// any host key.
func loadHostKeyCallback() (ssh.HostKeyCallback, error) {
	khPath, err := resolveKnownHostsPath()
	if err != nil {
		return nil, err
	}
	cb, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("%w: parse %s: %v", ErrKnownHosts, khPath, err)
	}
	return cb, nil
}

// resolveKnownHostsPath walks the precedence chain documented on
// loadHostKeyCallback and returns the first readable file, or an aggregated
// error listing every candidate that was tried.
func resolveKnownHostsPath() (string, error) {
	var tried []string
	if env := os.Getenv(envKnownHosts); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
		tried = append(tried, env+" (from "+envKnownHosts+")")
	}
	if _, err := os.Stat(systemKnownHosts); err == nil {
		return systemKnownHosts, nil
	}
	tried = append(tried, systemKnownHosts)

	home, err := os.UserHomeDir()
	if err == nil {
		userPath := filepath.Join(home, ".ssh", "known_hosts")
		if _, statErr := os.Stat(userPath); statErr == nil {
			return userPath, nil
		}
		tried = append(tried, userPath)
	} else {
		tried = append(tried, fmt.Sprintf("$HOME/.ssh/known_hosts (home lookup failed: %v)", err))
	}

	return "", fmt.Errorf("%w: no known_hosts file found, tried: %s",
		ErrKnownHosts, strings.Join(tried, ", "))
}

// dialContext wraps ssh.NewClientConn with a context-cancellable TCP dial.
func dialContext(ctx context.Context, network, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// runSCPSink drives the remote scp -t sink process through a single-file
// transfer. The protocol reference is openssh scp(1) + source code at
// openssh-portable/scp.c.
//
// Remote stderr is captured into a bounded buffer so errors returned from
// transferFile and session.Wait can include the peer's diagnostic text
// (e.g. "permission denied", "No such file or directory"). A background
// goroutine drains the stderr pipe; a WaitGroup ensures the capture is
// complete before any error is annotated.
func runSCPSink(ctx context.Context, session *ssh.Session, localPath string,
	info os.FileInfo, remotePath string) error {
	remoteDir, remoteFile := path.Split(remotePath)
	if remoteDir == "" {
		remoteDir = "."
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("scp sink: stdin: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("scp sink: stdout: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("scp sink: stderr: %w", err)
	}
	reader := bufio.NewReader(stdout)

	var stderrBuf bytes.Buffer
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		_, _ = io.Copy(&stderrBuf, io.LimitReader(stderr, stderrCaptureLimit))
		_, _ = io.Copy(io.Discard, stderr)
	}()

	cmd := "scp -t " + shellQuote(remoteDir)
	if err := session.Start(cmd); err != nil {
		_ = stdin.Close()
		stderrWG.Wait()
		return annotateWithStderr(fmt.Errorf("scp sink: start: %w", err), &stderrBuf)
	}

	if err := transferFile(ctx, stdin, reader, localPath, info, remoteFile); err != nil {
		// Close the session BEFORE Wait so the readAck helper goroutine (blocked
		// on r.ReadByte from the session's stdout) unblocks. Without this a
		// wedged peer would leave Wait blocking forever because stdout never
		// EOFs, re-introducing the daemon-worker hang this teardown prevents.
		_ = stdin.Close()
		_ = session.Close()
		_ = session.Wait()
		stderrWG.Wait()
		return annotateWithStderr(err, &stderrBuf)
	}

	if err := stdin.Close(); err != nil {
		_ = session.Close()
		_ = session.Wait()
		stderrWG.Wait()
		return annotateWithStderr(fmt.Errorf("scp sink: close stdin: %w", err), &stderrBuf)
	}
	waitErr := session.Wait()
	stderrWG.Wait()
	if waitErr != nil {
		return annotateWithStderr(fmt.Errorf("scp sink: remote exit: %w", waitErr), &stderrBuf)
	}
	return nil
}

// annotateWithStderr augments err with the trimmed contents of buf when it
// carries non-empty diagnostic text. The buffer itself is bounded by
// stderrCaptureLimit so the resulting error chain cannot grow without bound.
func annotateWithStderr(err error, buf *bytes.Buffer) error {
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(buf.String())
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: remote stderr: %s", err, msg)
}

// transferFile executes the scp sink dance: wait for ack, send C-line,
// wait for ack, stream body, send trailing null byte, wait for final ack.
func transferFile(ctx context.Context, stdin io.WriteCloser, reader *bufio.Reader,
	localPath string, info os.FileInfo, remoteFile string) error {
	if err := readAck(ctx, reader); err != nil {
		return err
	}

	mode := info.Mode().Perm()
	header := fmt.Sprintf("C%#o %d %s\n", mode, info.Size(), remoteFile)
	if _, err := io.WriteString(stdin, header); err != nil {
		return fmt.Errorf("scp sink: write header: %w", err)
	}
	if err := readAck(ctx, reader); err != nil {
		return err
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("scp sink: open %s: %w", localPath, err)
	}
	defer func() { _ = file.Close() }()

	if err := streamFile(ctx, stdin, file); err != nil {
		return err
	}

	if _, err := stdin.Write([]byte{0}); err != nil {
		return fmt.Errorf("scp sink: terminate file: %w", err)
	}
	return readAck(ctx, reader)
}

// streamFile copies file to stdin in 1 MiB chunks, honoring ctx cancellation.
func streamFile(ctx context.Context, stdin io.Writer, file *os.File) error {
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := file.Read(buf)
		if n > 0 {
			if _, werr := stdin.Write(buf[:n]); werr != nil {
				return fmt.Errorf("scp sink: write body: %w", werr)
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("scp sink: read body: %w", rerr)
		}
	}
}

// ackResult carries the outcome of a blocking ack read so the caller can
// multiplex it against ctx.Done() in readAck.
type ackResult struct {
	code byte
	// msg is populated only when the code is 1 or 2 (scp warning / fatal).
	msg    string
	msgErr error
	err    error
}

// readAck consumes a single scp acknowledgment byte. 0 = OK; 1 = warning
// (message follows, newline-terminated); 2 = fatal error.
//
// The blocking reads are performed in a helper goroutine so the call can be
// cancelled by ctx or by the ackTimeout guard without leaving the session
// wedged on a dead peer. On timeout or ctx cancellation the underlying reader
// is left in place: returning here aborts the sink dance and the caller tears
// the session down.
func readAck(ctx context.Context, r *bufio.Reader) error {
	resultCh := make(chan ackResult, 1)
	go func() {
		code, err := r.ReadByte()
		if err != nil {
			resultCh <- ackResult{err: err}
			return
		}
		switch code {
		case 0:
			resultCh <- ackResult{code: code}
		case 1, 2:
			msg, msgErr := r.ReadString('\n')
			resultCh <- ackResult{code: code, msg: msg, msgErr: msgErr}
		default:
			resultCh <- ackResult{code: code}
		}
	}()

	timer := time.NewTimer(ackTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("scp sink: read ack: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf("%w: read ack timed out after %s", ErrProtocol, ackTimeout)
	case res := <-resultCh:
		if res.err != nil {
			return fmt.Errorf("%w: read ack: %v", ErrProtocol, res.err)
		}
		switch res.code {
		case 0:
			return nil
		case 1, 2:
			if res.msgErr != nil && !errors.Is(res.msgErr, io.EOF) {
				return fmt.Errorf("%w: code=%d message=%q (message read error: %v)",
					ErrProtocol, res.code, res.msg, res.msgErr)
			}
			return fmt.Errorf("%w: code=%d message=%q", ErrProtocol, res.code, res.msg)
		default:
			return fmt.Errorf("%w: unexpected ack byte %#x", ErrProtocol, res.code)
		}
	}
}

// validateRemoteName rejects remoteName values that are empty, absolute, or
// contain path separators so attacker-controlled input cannot escape the
// configured remote base directory when the name is path.Join'd into the scp
// command line.
//
// filepath.Base stripping both POSIX and Windows separators already covers
// the "bare filename" invariant; a separate strings.Contains guard catches
// Windows "\\" on Unix hosts where filepath.Base is POSIX-only and would not
// treat backslash as a separator.
func validateRemoteName(remoteName string) error {
	if remoteName == "" {
		return errors.New("remoteName must not be empty")
	}
	if filepath.IsAbs(remoteName) {
		return fmt.Errorf("remoteName %q must not be absolute", remoteName)
	}
	if filepath.Base(remoteName) != remoteName {
		return fmt.Errorf("remoteName %q must be a bare filename", remoteName)
	}
	if strings.Contains(remoteName, "\\") {
		return fmt.Errorf("remoteName %q must not contain path separators", remoteName)
	}
	if remoteName == "." || remoteName == ".." {
		return fmt.Errorf("remoteName %q is not a valid filename", remoteName)
	}
	if strings.ContainsFunc(remoteName, unicode.IsControl) {
		return fmt.Errorf("remoteName %q must not contain control characters", remoteName)
	}
	return nil
}

// shellQuote returns s wrapped in single quotes, escaping any embedded single
// quotes so the result is safe for interpolation into a remote shell command.
func shellQuote(s string) string {
	var b []byte
	b = append(b, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b = append(b, '\'', '\\', '\'', '\'')
			continue
		}
		b = append(b, s[i])
	}
	b = append(b, '\'')
	return string(b)
}
