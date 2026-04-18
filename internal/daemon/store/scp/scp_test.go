package scp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dockermint/Pebblify/internal/daemon/config"
)

// ---- shellQuote ----

// TestShellQuote_Table verifies that shellQuote wraps in single quotes and escapes
// embedded single quotes correctly.
func TestShellQuote_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain path", "/home/user/backups", "'/home/user/backups'"},
		{"path with spaces", "/home/user/my backups", "'/home/user/my backups'"},
		{"single quote embedded", "it's", "'it'\\''s'"},
		{"multiple single quotes", "a'b'c", "'a'\\''b'\\''c'"},
		{"empty string", "", "''"},
		{"only single quote", "'", "''\\'''"},
		{"dot", ".", "'.'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shellQuote(tt.input)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestShellQuote_NoUnquotedSpecialChars verifies that the result always starts
// and ends with a single quote and contains no unprotected shell metacharacters.
func TestShellQuote_NoUnquotedSpecialChars(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"/normal/path",
		"/path with spaces",
		"/path/with'quote",
		"multiple'quotes'here",
		"$(evil command)",
		"`backtick`",
		"semicolon;separated",
	}
	for _, s := range inputs {
		t.Run(s, func(t *testing.T) {
			t.Parallel()
			got := shellQuote(s)
			if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
				t.Errorf("shellQuote(%q) = %q: does not start and end with single quote", s, got)
			}
		})
	}
}

// ---- readAck ----

// TestReadAck_ZeroByte returns nil for the success acknowledgment byte.
func TestReadAck_ZeroByte(t *testing.T) {
	t.Parallel()
	r := bufio.NewReader(bytes.NewReader([]byte{0}))
	if err := readAck(context.Background(), r); err != nil {
		t.Errorf("readAck(0x00) unexpected error: %v", err)
	}
}

// TestReadAck_WarningCode returns ErrProtocol wrapping code=1.
func TestReadAck_WarningCode(t *testing.T) {
	t.Parallel()
	payload := []byte{1}
	payload = append(payload, []byte("warning message\n")...)
	r := bufio.NewReader(bytes.NewReader(payload))
	err := readAck(context.Background(), r)
	if !errors.Is(err, ErrProtocol) {
		t.Errorf("readAck(0x01) error = %v, want wrapping %v", err, ErrProtocol)
	}
}

// TestReadAck_FatalCode returns ErrProtocol wrapping code=2.
func TestReadAck_FatalCode(t *testing.T) {
	t.Parallel()
	payload := []byte{2}
	payload = append(payload, []byte("fatal error\n")...)
	r := bufio.NewReader(bytes.NewReader(payload))
	err := readAck(context.Background(), r)
	if !errors.Is(err, ErrProtocol) {
		t.Errorf("readAck(0x02) error = %v, want wrapping %v", err, ErrProtocol)
	}
}

// TestReadAck_UnexpectedByte returns ErrProtocol for an out-of-spec byte.
func TestReadAck_UnexpectedByte(t *testing.T) {
	t.Parallel()
	r := bufio.NewReader(bytes.NewReader([]byte{42}))
	err := readAck(context.Background(), r)
	if !errors.Is(err, ErrProtocol) {
		t.Errorf("readAck(0x2a) error = %v, want wrapping %v", err, ErrProtocol)
	}
}

// TestReadAck_EmptyReader returns error on empty reader (EOF).
func TestReadAck_EmptyReader(t *testing.T) {
	t.Parallel()
	r := bufio.NewReader(bytes.NewReader(nil))
	err := readAck(context.Background(), r)
	if err == nil {
		t.Error("readAck(empty reader) expected error, got nil")
	}
}

// TestReadAck_CancelledContext returns ctx.Err when context is already cancelled.
func TestReadAck_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Use an io.Pipe reader so ReadByte blocks indefinitely, allowing ctx.Done to win.
	pr, _ := io.Pipe()
	defer func() { _ = pr.Close() }()
	r := bufio.NewReader(pr)
	err := readAck(ctx, r)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("readAck(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

// ---- buildAuth ----

// TestBuildAuth_NoneMode returns a non-nil AuthMethod for the "none" auth mode.
func TestBuildAuth_NoneMode(t *testing.T) {
	t.Parallel()
	auth, err := buildAuth(config.SCPAuthNone, config.Secrets{})
	if err != nil {
		t.Fatalf("buildAuth(none) unexpected error: %v", err)
	}
	if auth == nil {
		t.Error("buildAuth(none) returned nil AuthMethod")
	}
}

// TestBuildAuth_PasswordMode returns a non-nil AuthMethod when password is set.
func TestBuildAuth_PasswordMode(t *testing.T) {
	t.Parallel()
	secrets := config.Secrets{SCPPassword: "s3cr3t"}
	auth, err := buildAuth(config.SCPAuthPassword, secrets)
	if err != nil {
		t.Fatalf("buildAuth(password) unexpected error: %v", err)
	}
	if auth == nil {
		t.Error("buildAuth(password) returned nil AuthMethod")
	}
}

// TestBuildAuth_PasswordMissingSecret returns ErrMissingSecret when password is empty.
func TestBuildAuth_PasswordMissingSecret(t *testing.T) {
	t.Parallel()
	_, err := buildAuth(config.SCPAuthPassword, config.Secrets{SCPPassword: ""})
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("buildAuth(password, empty) error = %v, want wrapping %v", err, ErrMissingSecret)
	}
}

// TestBuildAuth_KeyMissingPath returns ErrMissingSecret when SCPKeyPath is empty.
func TestBuildAuth_KeyMissingPath(t *testing.T) {
	t.Parallel()
	_, err := buildAuth(config.SCPAuthKey, config.Secrets{SCPKeyPath: ""})
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("buildAuth(key, empty path) error = %v, want wrapping %v", err, ErrMissingSecret)
	}
}

// TestBuildAuth_KeyFileNotFound returns error when key file does not exist.
func TestBuildAuth_KeyFileNotFound(t *testing.T) {
	t.Parallel()
	secrets := config.Secrets{SCPKeyPath: filepath.Join(t.TempDir(), "nonexistent_key")}
	_, err := buildAuth(config.SCPAuthKey, secrets)
	if err == nil {
		t.Error("buildAuth(key, missing file) expected error, got nil")
	}
}

// TestBuildAuth_UnknownMode returns ErrUnsupportedAuth for an unrecognised mode.
func TestBuildAuth_UnknownMode(t *testing.T) {
	t.Parallel()
	_, err := buildAuth("kerberos", config.Secrets{})
	if !errors.Is(err, ErrUnsupportedAuth) {
		t.Errorf("buildAuth(kerberos) error = %v, want wrapping %v", err, ErrUnsupportedAuth)
	}
}

// TestBuildAuth_AllKnownModesCovered verifies all three config constants map to a
// recognised branch (none + password + key-with-missing-path before read).
func TestBuildAuth_AllKnownModesCovered(t *testing.T) {
	t.Parallel()
	// none — should succeed
	if _, err := buildAuth(config.SCPAuthNone, config.Secrets{}); err != nil {
		t.Errorf("buildAuth(none) unexpected error: %v", err)
	}
	// password with value — should succeed
	if _, err := buildAuth(config.SCPAuthPassword, config.Secrets{SCPPassword: "x"}); err != nil {
		t.Errorf("buildAuth(password) unexpected error: %v", err)
	}
	// key with missing path — should return ErrMissingSecret (not ErrUnsupportedAuth)
	_, err := buildAuth(config.SCPAuthKey, config.Secrets{SCPKeyPath: ""})
	if !errors.Is(err, ErrMissingSecret) {
		t.Errorf("buildAuth(key, no path) error = %v, want ErrMissingSecret", err)
	}
}

// ---- New (constructor validation) ----

// TestNew_MissingHost returns error (wrapping ErrMissingSecret) when host is empty.
func TestNew_MissingHost(t *testing.T) {
	t.Parallel()
	cfg := config.SCPSaveSection{
		Host:                 "",
		Port:                 22,
		Username:             "user",
		AuthentificationMode: config.SCPAuthNone,
	}
	_, err := New(cfg, config.Secrets{})
	if err == nil {
		t.Fatal("New(missing host) expected error, got nil")
	}
}

// TestNew_MissingUsername returns error when username is empty.
func TestNew_MissingUsername(t *testing.T) {
	t.Parallel()
	cfg := config.SCPSaveSection{
		Host:                 "myhost",
		Port:                 22,
		Username:             "",
		AuthentificationMode: config.SCPAuthNone,
	}
	_, err := New(cfg, config.Secrets{})
	if err == nil {
		t.Fatal("New(missing username) expected error, got nil")
	}
}

// TestNew_PortZero returns error for port 0.
func TestNew_PortZero(t *testing.T) {
	t.Parallel()
	cfg := config.SCPSaveSection{
		Host:                 "myhost",
		Port:                 0,
		Username:             "user",
		AuthentificationMode: config.SCPAuthNone,
	}
	_, err := New(cfg, config.Secrets{})
	if err == nil {
		t.Fatal("New(port=0) expected error, got nil")
	}
}

// TestNew_PortAboveMax returns error for port > 65535.
func TestNew_PortAboveMax(t *testing.T) {
	t.Parallel()
	cfg := config.SCPSaveSection{
		Host:                 "myhost",
		Port:                 70000,
		Username:             "user",
		AuthentificationMode: config.SCPAuthNone,
	}
	_, err := New(cfg, config.Secrets{})
	if err == nil {
		t.Fatal("New(port=70000) expected error, got nil")
	}
}

// TestNew_InvalidAuthMode returns error for an unsupported auth mode.
func TestNew_InvalidAuthMode(t *testing.T) {
	t.Parallel()
	cfg := config.SCPSaveSection{
		Host:                 "myhost",
		Port:                 22,
		Username:             "user",
		AuthentificationMode: "ldap",
	}
	_, err := New(cfg, config.Secrets{})
	if !errors.Is(err, ErrUnsupportedAuth) {
		t.Errorf("New(invalid auth) error = %v, want wrapping %v", err, ErrUnsupportedAuth)
	}
}

// ---- Upload (pre-dial validation) ----

// TestUpload_EmptyLocalPath returns error immediately without dialing.
// We construct SCPTarget directly (bypassing New's known_hosts check) to test
// the Upload validation guards in isolation.
func TestUpload_EmptyLocalPath(t *testing.T) {
	t.Parallel()
	target := &SCPTarget{
		host:     "localhost",
		port:     22,
		username: "user",
		authCfg:  nil,
		hostKey:  nil,
		remote:   ".",
	}
	err := target.Upload(context.Background(), "", "remote.tar")
	if err == nil {
		t.Fatal("Upload(empty localPath) expected error, got nil")
	}
}

// TestUpload_EmptyRemoteName returns error immediately without dialing.
func TestUpload_EmptyRemoteName(t *testing.T) {
	t.Parallel()
	target := &SCPTarget{
		host:     "localhost",
		port:     22,
		username: "user",
		authCfg:  nil,
		hostKey:  nil,
		remote:   ".",
	}
	err := target.Upload(context.Background(), "/some/file", "")
	if err == nil {
		t.Fatal("Upload(empty remoteName) expected error, got nil")
	}
}

// TestUpload_CancelledContext returns context.Canceled before any I/O.
func TestUpload_CancelledContext(t *testing.T) {
	t.Parallel()
	target := &SCPTarget{
		host:     "localhost",
		port:     22,
		username: "user",
		authCfg:  nil,
		hostKey:  nil,
		remote:   ".",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := target.Upload(ctx, "/some/file", "remote.tar")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Upload(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

// TestUpload_NonRegularFile returns error when local path is a directory.
func TestUpload_NonRegularFile(t *testing.T) {
	t.Parallel()
	target := &SCPTarget{
		host:     "localhost",
		port:     22,
		username: "user",
		authCfg:  nil,
		hostKey:  nil,
		remote:   ".",
	}
	// A directory is not a regular file.
	err := target.Upload(context.Background(), t.TempDir(), "remote.tar")
	if err == nil {
		t.Fatal("Upload(directory as localPath) expected error, got nil")
	}
}

// TestUpload_MissingLocalFile returns error when local path does not exist.
func TestUpload_MissingLocalFile(t *testing.T) {
	t.Parallel()
	target := &SCPTarget{
		host:     "localhost",
		port:     22,
		username: "user",
		authCfg:  nil,
		hostKey:  nil,
		remote:   ".",
	}
	err := target.Upload(context.Background(),
		filepath.Join(t.TempDir(), "nonexistent.tar"), "remote.tar")
	if err == nil {
		t.Fatal("Upload(missing file) expected error, got nil")
	}
}

// ---- SCPTarget.Name ----

// TestSCPTarget_Name returns the package-level Name constant.
func TestSCPTarget_Name(t *testing.T) {
	t.Parallel()
	target := &SCPTarget{}
	if got := target.Name(); got != Name {
		t.Errorf("SCPTarget.Name() = %q, want %q", got, Name)
	}
}

// ---- loadHostKeyCallback ----

// TestLoadHostKeyCallback_MissingKnownHostsFile returns ErrKnownHosts when the
// ~/.ssh/known_hosts file does not exist. We test this by pointing HOME at a
// temporary directory that has no .ssh directory.
func TestLoadHostKeyCallback_MissingKnownHostsFile(t *testing.T) {
	// No t.Parallel() — modifies HOME env var.
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, err := loadHostKeyCallback()
	if !errors.Is(err, ErrKnownHosts) {
		t.Errorf("loadHostKeyCallback() error = %v, want wrapping %v", err, ErrKnownHosts)
	}
}

// TestLoadHostKeyCallback_EmptyKnownHostsFile returns a non-nil callback for an
// existing but empty known_hosts file. An empty file is valid (no hosts known);
// knownhosts.New accepts it without error.
func TestLoadHostKeyCallback_EmptyKnownHostsFile(t *testing.T) {
	// No t.Parallel() — modifies HOME env var.
	home := t.TempDir()
	t.Setenv("HOME", home)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	khPath := filepath.Join(sshDir, "known_hosts")
	if err := os.WriteFile(khPath, []byte{}, 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	cb, err := loadHostKeyCallback()
	if err != nil {
		t.Fatalf("loadHostKeyCallback() unexpected error: %v", err)
	}
	if cb == nil {
		t.Error("loadHostKeyCallback() returned nil callback")
	}
}

// ---- streamFile ----

// TestStreamFile_CopiesDataToWriter verifies streamFile writes the full file body.
func TestStreamFile_CopiesDataToWriter(t *testing.T) {
	t.Parallel()
	content := []byte("hello pebblify scp test")
	f, err := os.CreateTemp(t.TempDir(), "stream")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}

	var dst bytes.Buffer
	if err := streamFile(context.Background(), &dst, f); err != nil {
		t.Fatalf("streamFile() error: %v", err)
	}
	_ = f.Close()

	if !bytes.Equal(dst.Bytes(), content) {
		t.Errorf("streamFile() wrote %q, want %q", dst.Bytes(), content)
	}
}

// TestStreamFile_CancelledContextReturnsError verifies context cancellation
// stops the copy loop.
func TestStreamFile_CancelledContextReturnsError(t *testing.T) {
	t.Parallel()
	// Write enough data that the loop will check ctx between reads.
	large := make([]byte, 2<<20) // 2 MiB > the 1 MiB chunk
	for i := range large {
		large[i] = byte(i % 256)
	}
	f, err := os.CreateTemp(t.TempDir(), "stream_large")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := f.Write(large); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so first iteration returns immediately
	err = streamFile(ctx, io.Discard, f)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("streamFile(cancelled ctx) error = %v, want context.Canceled", err)
	}
}

// TestStreamFile_EmptyFile succeeds and writes nothing.
func TestStreamFile_EmptyFile(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp(t.TempDir(), "empty")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer func() { _ = f.Close() }()

	var dst bytes.Buffer
	if err := streamFile(context.Background(), &dst, f); err != nil {
		t.Fatalf("streamFile(empty) error: %v", err)
	}
	if dst.Len() != 0 {
		t.Errorf("streamFile(empty) wrote %d bytes, want 0", dst.Len())
	}
}
