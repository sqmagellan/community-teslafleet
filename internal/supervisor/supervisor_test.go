package supervisor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A crashing process is restarted with backoff rather than left dead.
func TestSupervisor_RestartsOnExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs")
	s := New(quietLogger())
	s.Add(Process{
		Name: "crasher",
		Path: "/bin/sh",
		Args: []string{"-c", "echo run >> " + marker + "; exit 1"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	// minBackoff is 1s: runs at ~0s, ~1s, ~3s. Wait long enough for at least two.
	time.Sleep(2500 * time.Millisecond)
	cancel()
	s.Wait()

	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	runs := 0
	for _, c := range b {
		if c == '\n' {
			runs++
		}
	}
	if runs < 2 {
		t.Errorf("expected the crashing process to be restarted (>=2 runs), got %d", runs)
	}
}

// Cancelling the context terminates a long-running process promptly.
func TestSupervisor_GracefulStop(t *testing.T) {
	s := New(quietLogger())
	s.Add(Process{Name: "sleeper", Path: "/bin/sh", Args: []string{"-c", "sleep 60"}})

	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	time.Sleep(300 * time.Millisecond) // let it start
	cancel()

	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(termGrace + 3*time.Second):
		t.Fatal("supervisor did not stop the process within the grace period")
	}
}

func TestChildEnvDropsGatewaySecrets(t *testing.T) {
	got := childEnv([]string{
		"PATH=/usr/bin",
		"TGW_TESLA_CLIENT_SECRET=s",
		"TGW_TESLA_REFRESH_TOKEN=r",
		"SUPERVISOR_TOKEN=x",
		"TZ=UTC",
	})
	if strings.Join(got, " ") != "PATH=/usr/bin TZ=UTC" {
		t.Errorf("child env = %v", got)
	}
}
