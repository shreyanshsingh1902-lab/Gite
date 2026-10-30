// Package gateway exposes git transport into gite.
//
// The ssh server is built in (x/crypto/ssh) so a forge is a single binary
// with zero system configuration: `gite serve` is everything a machine
// needs. Public key auth resolves the connecting key to a gite account,
// then every request is authorized through auth.Require before git runs.
package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"log/slog"
	"net"

	"golang.org/x/crypto/ssh"

	"gite/internal/auth"
	"gite/internal/config"
	"gite/internal/db"
	"gite/internal/git"
	"gite/internal/repository"
)

// SSHServer is the git-over-ssh transport.
type SSHServer struct {
	cfg   config.Config
	conn  *db.DB
	repos *repository.Store
	log   *slog.Logger
	priv  ssh.Signer
}

// NewSSHServer builds the transport for one data directory, generating the
// host key on first use.
func NewSSHServer(cfg config.Config, conn *db.DB, log *slog.Logger) (*SSHServer, error) {
	priv, err := loadOrCreateHostKey(cfg.Home)
	if err != nil {
		return nil, err
	}
	return &SSHServer{
		cfg:   cfg,
		conn:  conn,
		repos: repository.NewStore(conn, cfg.RepoRoot),
		log:   log,
		priv:  priv,
	}, nil
}

func loadOrCreateHostKey(home string) (ssh.Signer, error) {
	path := filepath.Join(home, "ssh_host_ed25519_key")
	if raw, err := os.ReadFile(path); err == nil {
		seed, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if decErr == nil && len(seed) == ed25519.PrivateKeySize {
			if signer, err := ssh.NewSignerFromKey(ed25519.PrivateKey(seed)); err == nil {
				return signer, nil
			}
		}
		// fall through to regeneration; an unreadable host key is replaced
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		return nil, fmt.Errorf("save host key %s: %w", path, err)
	}
	return ssh.NewSignerFromKey(priv)
}

// ListenAndServe blocks until ctx is cancelled.
func (s *SSHServer) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.SSHAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.SSHAddr, err)
	}
	s.log.Info("git transport listening",
		"addr", s.cfg.SSHAddr,
		"host_key_fingerprint", ssh.FingerprintSHA256(s.priv.PublicKey()))

	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			wg.Wait()
			return fmt.Errorf("accept: %w", err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.serveConn(conn)
		}()
	}
}

func (s *SSHServer) serveConn(conn net.Conn) {
	srvCfg := &ssh.ServerConfig{
		BannerCallback: func(ssh.ConnMetadata) string {
			return "gite git forge - registered keys only\n"
		},
		PublicKeyCallback: func(md ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			marker := ssh.FingerprintSHA256(key)
			_, user, err := auth.UserKeyByFingerprint(context.Background(), s.conn, marker)
			if err != nil {
				if errors.Is(err, auth.ErrNotFound) {
					s.log.Warn("ssh auth rejected: unregistered key",
						"fingerprint", marker, "remote", md.RemoteAddr().String())
					return nil, fmt.Errorf("this public key is not registered on this gite")
				}
				return nil, err
			}
			s.log.Info("ssh auth ok", "user", user.Username, "fingerprint", marker,
				"remote", md.RemoteAddr().String())
			return &ssh.Permissions{
				Extensions: map[string]string{"gite.user": user.Username},
			}, nil
		},
	}
	srvConn, chans, reqs, err := ssh.NewServerConn(conn, srvCfg)
	if err != nil {
		return // lib reported the failure; handshake detail stays client-side
	}
	defer srvConn.Close()
	go ssh.DiscardRequests(reqs)

	user := srvConn.Permissions.Extensions["gite.user"]
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "gite: only session channels are supported")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.serviceSession(user, ch, chReqs)
	}
}

type execRequest struct{ Command string }
type envRequest struct {
	Name  string
	Value string
}

// serviceSession drains session requests until the channel closes.
// Only exec starts git; everything else is politely refused.
func (s *SSHServer) serviceSession(user string, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	var (
		envMu  sync.Mutex
		envCol []string
	)
	pushEnv := func(name, value string) {
		envMu.Lock()
		envCol = append(envCol, name+"="+value)
		envMu.Unlock()
	}

	for req := range reqs {
		switch req.Type {
		case "env":
			var e envRequest
			if err := ssh.Unmarshal(req.Payload, &e); err == nil && e.Name == "GIT_PROTOCOL" {
				pushEnv(e.Name, e.Value)
			}
			if req.WantReply() {
				req.Reply(true, nil)
			}
		case "exec":
			var c execRequest
			if err := ssh.Unmarshal(req.Payload, &c); err == nil {
				envMu.Lock()
				env := append([]string(nil), envCol...)
				envMu.Unlock()
				if req.WantReply() {
					req.Reply(true, nil)
				}
				s.runGit(user, ch, c.Command, env)
				return
			}
			if req.WantReply() {
				req.Reply(false, nil)
			}
		default:
			if req.WantReply() {
				req.Reply(false, nil)
			}
		}
	}
}

// runGit authorizes and executes one git command, then reports its exit.
func (s *SSHServer) runGit(user string, ch ssh.Channel, rawCommand string, extraEnv []string) {
	finish := func(exit uint32, msg string) {
		if msg != "" {
			fmt.Fprintf(ch.Stderr(), "gite: %s\r\n", msg)
		}
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exit}))
		ch.Close()
	}

	repo, verb, err := s.authorize(user, rawCommand)
	if err != nil {
		finish(1, err.Error())
		return
	}

	gitBin, err := git.Binary()
	if err != nil {
		finish(127, err.Error())
		return
	}
	cmd := exec.CommandContext(context.Background(), gitBin, verb, s.repos.Path(repo))
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()

	start := time.Now()
	if err := cmd.Start(); err != nil {
		finish(127, err.Error())
		return
	}
	waitErr := cmd.Wait()
	code := 0
	var ee *exec.ExitError
	if waitErr != nil && errors.As(waitErr, &ee) {
		code = ee.ExitCode()
	} else if waitErr != nil {
		code = 1
	}
	if code < 0 {
		code = 255
	}
	s.log.Info("git handled", "user", user, "command", verb,
		"repository", repo.FullName(), "exit", code, "duration", time.Since(start))
	finish(uint32(code), "")
}

var pathSegRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

// authorize turns one raw ssh exec command into an authorized git verb
// plus repository, or a client-safe error.
func (s *SSHServer) authorize(user, raw string) (*repository.Repo, string, error) {
	fields, err := shSplit(raw)
	if err != nil {
		return nil, "", fmt.Errorf("malformed request: %v", err)
	}
	if len(fields) != 2 {
		return nil, "", fmt.Errorf("expected: git-upload-pack '/owner/name.git' or git-receive-pack '/owner/name.git'")
	}
	verb, path := fields[0], fields[1]

	var want auth.Role
	switch verb {
	case "git-upload-pack":
		want = auth.RoleRead
	case "git-receive-pack":
		want = auth.RoleWrite
	default:
		return nil, "", fmt.Errorf("only git-upload-pack and git-receive-pack are allowed")
	}

	p := strings.TrimSuffix(strings.TrimPrefix(path, "/"), ".git")
	parts := strings.Split(p, "/")
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("repository path must be owner/name")
	}
	owner, name := strings.ToLower(parts[0]), strings.ToLower(parts[1])
	if !pathSegRe.MatchString(owner) || !pathSegRe.MatchString(name) {
		return nil, "", fmt.Errorf("malformed repository path")
	}

	ctx := context.Background()
	repo, getErr := s.repos.GetByOwnerName(ctx, owner, name)
	if getErr != nil {
		return nil, "", fmt.Errorf("repository not found")
	}
	u, userErr := auth.GetUserByUsername(ctx, s.conn, user)
	if userErr != nil {
		return nil, "", fmt.Errorf("repository not found")
	}
	if reqErr := auth.Require(ctx, s.conn, u, repo, want); reqErr != nil {
		if errors.Is(reqErr, auth.ErrNotFound) {
			return nil, "", fmt.Errorf("repository not found")
		}
		return nil, "", fmt.Errorf("%s access denied on %s for %s", want, repo.FullName(), user)
	}
	if _, statErr := os.Stat(filepath.Join(s.repos.Path(repo), "HEAD")); statErr != nil {
		return nil, "", fmt.Errorf("repository storage missing on disk: %s", s.repos.Path(repo))
	}
	return repo, verb, nil
}

// shSplit splits a shell-ish command line into words. ' and " are handled;
// backslashes are not, since git clients never send them here.
func shSplit(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	var quote byte
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, b.String())
			b.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				b.WriteByte(c)
			}
			i++
		case c == '\'' || c == '"':
			quote = c
			inWord = true
			i++
		case c == ' ' || c == '\t':
			flush()
			i++
		default:
			b.WriteByte(c)
			inWord = true
			i++
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote")
	}
	flush()
	return words, nil
}
