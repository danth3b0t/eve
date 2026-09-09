//go:build linux || darwin

// Package git is the checked local Git boundary. It never runs application
// commands, fetches refs, installs dependencies, or exposes subprocess output in
// diagnostics. Normal Git checkout filters are NOT an untrusted-code sandbox.
package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"eve/internal/domain"
)

const maxOutput = 8 << 20

type Command struct {
	Dir  string
	Args []string
	Env  []string
}

type Result struct {
	Output   []byte
	ExitCode int
}

// Runner is injectable for boundary/fault tests. Output is data only; the client
// applies bounds, allowed exit statuses and sanitized diagnostics regardless of
// which runner supplied it.
type Runner func(context.Context, Command) (Result, error)

type Client struct {
	Runner Runner
}

func New() (*Client, error) {
	exe, err := exec.LookPath("git")
	if err != nil {
		return nil, problem("E_GIT_REQUIRED", "Git must be available on PATH", "")
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, problem("E_GIT_REQUIRED", "cannot resolve Git executable", "")
	}
	return &Client{Runner: executableRunner(exe)}, nil
}

func problem(code, message, path string) error {
	return &domain.Error{Code: code, Message: message, Path: path}
}

// Keep HOME/config paths for the user's ordinary Git configuration and ignore
// rules. Do not inherit GIT_DIR, GIT_CONFIG_*, trace variables, shell loaders,
// management/deployment tokens or arbitrary application environment.
func gitEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, "LANG=C", "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1")
}

func (c *Client) run(ctx context.Context, dir string, args ...string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if c == nil || c.Runner == nil {
		return Result{}, problem("E_GIT_REQUIRED", "Git runner is unavailable", "")
	}
	flags := []string{"--no-pager", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	r, err := c.Runner(ctx, Command{Dir: dir, Args: append(flags, args...), Env: gitEnvironment()})
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		var d *domain.Error
		if errors.As(err, &d) && d.Code == "E_GIT_OUTPUT_LIMIT" {
			return Result{}, problem(d.Code, "Git output exceeds the supported bound", dir)
		}
		return Result{}, problem("E_GIT_EXEC", "Git could not complete; subprocess output withheld", dir)
	}
	if len(r.Output) > maxOutput {
		return Result{}, problem("E_GIT_OUTPUT_LIMIT", "Git output exceeds the supported bound", dir)
	}
	return r, nil
}

func (c *Client) read(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		return nil, problem("E_GIT_READ", "Git metadata could not be read; no absence is assumed", dir)
	}
	return r.Output, nil
}

// scalar removes exactly Git's final LF, not path/branch whitespace.
func scalar(data []byte) (string, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.IndexByte(data, 0) >= 0 {
		return "", problem("E_GIT_FORMAT", "unsupported Git metadata output", "")
	}
	return string(data[:len(data)-1]), nil
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  bool
	cancel context.CancelFunc
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > maxOutput-b.buffer.Len() {
		b.limit = true
		b.cancel()
		return 0, problem("E_GIT_OUTPUT_LIMIT", "Git output exceeds the supported bound", "")
	}
	return b.buffer.Write(p)
}

func executableRunner(exe string) Runner {
	return func(ctx context.Context, in Command) (Result, error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, in.Args...)
		cmd.Dir, cmd.Env = in.Dir, in.Env
		cmd.Stderr = io.Discard
		out := boundedOutput{cancel: cancel}
		cmd.Stdout = &out
		// Terminate EVE's own Git subprocess group, including checkout filters,
		// on cancellation. This does not supervise the application's launcher.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		cmd.WaitDelay = 2 * time.Second
		err := cmd.Run()
		if out.limit {
			return Result{}, problem("E_GIT_OUTPUT_LIMIT", "Git output exceeds the supported bound", "")
		}
		if err == nil {
			return Result{Output: out.buffer.Bytes()}, nil
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return Result{ExitCode: exit.ExitCode()}, nil // failure output is never useful data
		}
		return Result{}, err
	}
}

func safeArgument(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.HasPrefix(value, "-") && !strings.ContainsRune(value, 0)
}
