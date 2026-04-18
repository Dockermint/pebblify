// Package scp implements an SSH/SCP-backed store.Target.
//
// The SCP wire protocol is inlined on top of golang.org/x/crypto/ssh; no
// third-party SCP client library is imported. Host keys are verified against
// the operator's ~/.ssh/known_hosts file; connecting to an unknown host is
// a hard error (no trust-on-first-use prompt).
package scp

import (
	"bufio"
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
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// Name is the Target identifier reported by SCPTarget.Name.
const Name = "scp"

// dialTimeout bounds the TCP + SSH handshake duration.
const dialTimeout = 30 * time.Second

// sessionTimeout bounds a single SCP upload session (handshake + transfer).
const sessionTimeout = 60 * time.Minute

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
	reader := bufio.NewReader(stdout)

	cmd := "scp -t " + shellQuote(remoteDir)
	if err := session.Start(cmd); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("scp sink: start: %w", err)
	}

	if err := transferFile(ctx, stdin, reader, localPath, info, remoteFile); err != nil {
		_ = stdin.Close()
		_ = session.Wait()
		return err
	}

	if err := stdin.Close(); err != nil {
		_ = session.Wait()
		return fmt.Errorf("scp sink: close stdin: %w", err)
	}
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp sink: remote exit: %w", err)
	}
	return nil
}

// transferFile executes the scp sink dance: wait for ack, send C-line,
// wait for ack, stream body, send trailing null byte, wait for final ack.
func transferFile(ctx context.Context, stdin io.WriteCloser, reader *bufio.Reader,
	localPath string, info os.FileInfo, remoteFile string) error {
	if err := readAck(reader); err != nil {
		return err
	}

	mode := info.Mode().Perm()
	header := fmt.Sprintf("C%#o %d %s\n", mode, info.Size(), remoteFile)
	if _, err := io.WriteString(stdin, header); err != nil {
		return fmt.Errorf("scp sink: write header: %w", err)
	}
	if err := readAck(reader); err != nil {
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
	return readAck(reader)
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

// readAck consumes a single scp acknowledgment byte. 0 = OK; 1 = warning
// (message follows, newline-terminated); 2 = fatal error.
func readAck(r *bufio.Reader) error {
	code, err := r.ReadByte()
	if err != nil {
		return fmt.Errorf("scp sink: read ack: %w", err)
	}
	switch code {
	case 0:
		return nil
	case 1, 2:
		msg, _ := r.ReadString('\n')
		return fmt.Errorf("%w: code=%d message=%q", ErrProtocol, code, msg)
	default:
		return fmt.Errorf("%w: unexpected ack byte %#x", ErrProtocol, code)
	}
}

// validateRemoteName rejects remoteName values that are empty, absolute, or
// contain path separators so attacker-controlled input cannot escape the
// configured remote base directory when the name is path.Join'd into the scp
// command line.
func validateRemoteName(remoteName string) error {
	if remoteName == "" {
		return errors.New("remoteName must not be empty")
	}
	if filepath.IsAbs(remoteName) || strings.HasPrefix(remoteName, "/") {
		return fmt.Errorf("remoteName %q must not be absolute", remoteName)
	}
	if filepath.Base(remoteName) != remoteName {
		return fmt.Errorf("remoteName %q must be a bare filename", remoteName)
	}
	if strings.ContainsAny(remoteName, "/\\") {
		return fmt.Errorf("remoteName %q must not contain path separators", remoteName)
	}
	if remoteName == "." || remoteName == ".." {
		return fmt.Errorf("remoteName %q is not a valid filename", remoteName)
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
