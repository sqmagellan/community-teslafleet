// Package supervisor runs the upstream Tesla components (fleet-telemetry, and the
// vehicle-command proxy when commands are enabled) as child processes inside the
// gateway container — the "all-in-one" mode the Home Assistant add-on uses so one
// add-on yields the whole stack. The gateway acts as the container's init: it
// starts each child, streams its output to the gateway log, restarts it with
// backoff if it crashes, and terminates it cleanly on shutdown.
package supervisor

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Process describes a child process to supervise.
type Process struct {
	Name string   // log label, e.g. "fleet-telemetry"
	Path string   // absolute binary path
	Args []string // command-line arguments
	Env  []string // extra env, appended to the parent environment
	Dir  string   // working directory (optional)
}

type Supervisor struct {
	log   *slog.Logger
	procs []Process
	wg    sync.WaitGroup
}

func New(log *slog.Logger) *Supervisor { return &Supervisor{log: log} }

// Add registers a process to be started by Start.
func (s *Supervisor) Add(p Process) { s.procs = append(s.procs, p) }

// Len reports how many processes are registered.
func (s *Supervisor) Len() int { return len(s.procs) }

// Start launches every registered process under a restart-on-exit loop and returns
// immediately. Children are terminated when ctx is cancelled; Wait blocks until they
// have all exited.
func (s *Supervisor) Start(ctx context.Context) {
	for _, p := range s.procs {
		s.wg.Add(1)
		go func(p Process) {
			defer s.wg.Done()
			s.supervise(ctx, p)
		}(p)
	}
}

// Wait blocks until all supervised processes have exited (after ctx cancellation).
func (s *Supervisor) Wait() { s.wg.Wait() }

const (
	minBackoff   = time.Second
	maxBackoff   = 30 * time.Second
	healthyAfter = 30 * time.Second // a run lasting this long resets the backoff
	termGrace    = 8 * time.Second  // SIGTERM → SIGKILL grace on shutdown
)

func (s *Supervisor) supervise(ctx context.Context, p Process) {
	backoff := minBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := s.runOnce(ctx, p)
		if ctx.Err() != nil {
			s.log.Info("supervised process stopped", "name", p.Name)
			return // cancellation — expected exit, no restart
		}
		ran := time.Since(start)
		if ran > healthyAfter {
			backoff = minBackoff // it stayed up a while; treat the crash as fresh
		}
		s.log.Warn("supervised process exited — restarting",
			"name", p.Name, "ran", ran.Round(time.Second), "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce starts the process, streams stdout/stderr to the log, and waits for it to
// exit. On ctx cancellation it sends SIGTERM and, after a grace period, SIGKILL.
func (s *Supervisor) runOnce(ctx context.Context, p Process) error {
	cmd := exec.Command(p.Path, p.Args...)
	cmd.Env = append(childEnv(os.Environ()), p.Env...)
	cmd.Dir = p.Dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.log.Info("supervised process started", "name", p.Name, "pid", cmd.Process.Pid)

	var outWG sync.WaitGroup
	outWG.Add(2)
	go func() { defer outWG.Done(); s.pipe(p.Name, stdout) }()
	go func() { defer outWG.Done(); s.pipe(p.Name, stderr) }()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		outWG.Wait()
		return err
	case <-ctx.Done():
		s.terminate(cmd)
		err := <-done
		outWG.Wait()
		return err
	}
}

// childEnv drops the gateway's own settings before a child sees the
// environment. TGW_* can carry the Tesla client secret, the refresh token, the
// MQTT password and the debug token, and neither fleet-telemetry nor the
// command proxy needs any of them. SUPERVISOR_TOKEN is the HA add-on's API
// credential and goes too.
func childEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TGW_") || strings.HasPrefix(kv, "SUPERVISOR_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// terminate asks the process to stop (SIGTERM) and force-kills it if it overstays
// the grace period. It does not call Wait — the caller drains the done channel.
func (s *Supervisor) terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	go func() {
		time.Sleep(termGrace)
		_ = cmd.Process.Kill() // no-op once the process has already exited
	}()
}

// pipe forwards a child's output stream to the gateway log, one line per record.
func (s *Supervisor) pipe(name string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		s.log.Info("child", "proc", name, "line", sc.Text())
	}
}
