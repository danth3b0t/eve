package ports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"eve/internal/config"
	"eve/internal/domain"
	"eve/internal/state"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const manifest = `version = 1
[workspace]
port_block_size = 4
[services.web]
path = "."
env_file = ".env.local"
port = "PORT"
[services.web.ports.hmr]
env = "HMR_PORT"
[services.admin]
path = "."
env_file = ".admin.env.local"
port = "PORT"
`

func storeFixture(t *testing.T) (*state.Store, string, state.Repository) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "registry")
	s, err := state.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	r := register(t, s)
	return s, root, r
}

func register(t *testing.T, s *state.Store) state.Repository {
	t.Helper()
	source := t.TempDir()
	common := filepath.Join(source, ".git")
	if err := os.Mkdir(common, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := s.RegisterRepository(t.Context(), common, source, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func intent(t *testing.T, s *state.Store, r state.Repository, text string, low, high int) (*state.LockedWorkspace, string) {
	t.Helper()
	id := uuid.NewString()
	w, err := s.LockWorkspace(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	_, err = w.BeginCreate(t.Context(), state.CreateRequest{RepositoryID: r.ID, Branch: id, Path: filepath.Join(filepath.Dir(r.SourcePath), id), HeadOID: strings.Repeat("a", 40), Manifest: []byte(text), Ports: config.UserConfig{MinPort: low, MaxPort: high}})
	if err != nil {
		t.Fatal(err)
	}
	return w, id
}

func hasCode(t *testing.T, err error, want string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(err, &d) || d.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
}

func TestExclusiveTCPProbe(t *testing.T) {
	for _, tc := range []struct{ network, address string }{{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"}} {
		t.Run(tc.network, func(t *testing.T) {
			listener, err := net.Listen(tc.network, tc.address)
			if err != nil {
				if tc.network == "tcp6" && (errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.EADDRNOTAVAIL)) {
					t.Skip("host has no IPv6 loopback")
				}
				t.Fatal(err)
			}
			defer listener.Close()
			port := listener.Addr().(*net.TCPAddr).Port
			hasCode(t, ProbeTCP(t.Context(), port), "E_PORT_OCCUPIED")
			listener.Close()
			if err := ProbeTCP(t.Context(), port); err != nil {
				t.Fatal("port did not become available after listener stopped", err)
			}
			// The probe must have released its sockets, not leased the port.
			listener, err = net.Listen(tc.network, listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			listener.Close()
		})
	}
	for _, port := range []int{0, -1, 65536} {
		hasCode(t, ProbeTCP(t.Context(), port), "E_PORT_RANGE")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(ProbeTCP(ctx, 31000), context.Canceled) {
		t.Fatal("cancellation not preserved")
	}
}

func TestReserveSkipsOccupiedTailAndPreservesSlots(t *testing.T) {
	s, _, r := storeFixture(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	occupied := listener.Addr().(*net.TCPAddr).Port
	// The busy port is in the unused tail, not a declared endpoint: probing
	// just listeners would incorrectly accept this candidate.
	low := occupied - 3
	if low < 1 || occupied > 65520 {
		t.Skip("ephemeral port too close to a range boundary")
	}
	w, id := intent(t, s, r, manifest, low, 65535)
	a, err := Reserve(t.Context(), w, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Base <= occupied || !a.Ready || a.Size != 4 {
		t.Fatalf("accepted a block containing an occupied tail: %+v", a)
	}
	for i, want := range []struct{ service, name string }{{"admin", "primary"}, {"web", "hmr"}, {"web", "primary"}} {
		if i >= len(a.Endpoints) || a.Endpoints[i].Service != want.service || a.Endpoints[i].Name != want.name || a.Endpoints[i].Slot != i || a.Endpoints[i].Port != a.Base+i {
			t.Fatalf("unexpected initial endpoint slots: %+v", a.Endpoints)
		}
	}
	// A later conflict must leave the selected URL/port frozen, not trigger
	// another search. A listening port does not identify the service's owner.
	later, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", a.Endpoints[0].Port))
	if err != nil {
		t.Fatal(err)
	}
	defer later.Close()
	hasCode(t, CheckEndpoints(t.Context(), a, nil), "E_PORT_OCCUPIED")
	again, err := Reserve(t.Context(), w, nil)
	if err != nil || !reflect.DeepEqual(a, again) {
		t.Fatal("finalized allocation moved after conflict", err)
	}
	persisted, err := s.Allocation(t.Context(), id)
	if err != nil || !reflect.DeepEqual(a, persisted) {
		t.Fatal("allocation changed in storage", err)
	}
}

func TestAllocationBoundariesAndUnexpectedProbeFailure(t *testing.T) {
	s, _, r := storeFixture(t)
	w, _ := intent(t, s, r, strings.Replace(manifest, "size = 4", "size = 3", 1), 65533, 65535)
	a, err := Reserve(t.Context(), w, func(context.Context, int) error { return nil })
	if err != nil || a.Base != 65533 || a.Endpoints[2].Port != 65535 {
		t.Fatalf("inclusive high boundary: %+v %v", a, err)
	}
	other, _ := intent(t, s, r, strings.Replace(manifest, "size = 4", "size = 3", 1), 65533, 65535)
	_, err = Reserve(t.Context(), other, nil)
	hasCode(t, err, "E_PORT_EXHAUSTED")

	broken, brokenID := intent(t, s, r, manifest, 31000, 31099)
	_, err = Reserve(t.Context(), broken, func(context.Context, int) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	pending, err := s.Allocation(t.Context(), brokenID)
	if err != nil || pending.Size != 4 || pending.Ready || len(pending.Endpoints) != 0 {
		t.Fatalf("cancellation lost intent or falsely finalized it: %+v %v", pending, err)
	}
	var probed []int
	resumed, err := Reserve(t.Context(), broken, func(_ context.Context, p int) error { probed = append(probed, p); return nil })
	if err != nil || resumed.Base != pending.Base || !reflect.DeepEqual(probed, []int{31000, 31001, 31002, 31003}) {
		t.Fatalf("resume did not re-probe the incomplete candidate: %+v %v", probed, err)
	}
}

func TestNoListenerDoesNotReserveOrProbe(t *testing.T) {
	s, _, r := storeFixture(t)
	w, _ := intent(t, s, r, `version=1
[services.config]
path="."
env_file=".env.local"
[services.config.env]
LABEL="local"
`, 40000, 40000)
	a, err := Reserve(t.Context(), w, func(context.Context, int) error { t.Fatal("no-listener manifest probed a socket"); return nil })
	if err != nil || a.Base != 0 || a.Size != 0 || len(a.Endpoints) != 0 || !a.Ready {
		t.Fatalf("no-listener allocation: %+v %v", a, err)
	}
}

func TestProbeDoesNotHoldSQLTransaction(t *testing.T) {
	s, root, r := storeFixture(t)
	otherStore, err := state.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer otherStore.Close()
	w, _ := intent(t, s, r, manifest, 34000, 34099)
	other, _ := intent(t, otherStore, r, manifest, 34000, 34099)
	_, err = Reserve(t.Context(), w, func(_ context.Context, port int) error {
		if port != 34000 {
			return nil
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		b, err := other.Candidate(ctx, 0)
		if err != nil {
			return err // A write transaction held during probing blocks this.
		}
		if b.Base < 34004 {
			t.Fatal("first candidate was not durably claimed before probing")
		}
		return other.RejectCandidate(ctx, b.Base)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Subprocesses exercise OS locks and the real driver, not goroutine-only
// coordination. A stdin barrier starts concurrent workers together.
func TestAllocatorProcess(t *testing.T) {
	mode := os.Getenv("EVE_TEST_ALLOCATOR")
	if mode == "" {
		return
	}
	unix.Umask(0) // New files must be private even under a permissive user umask.
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(t.Context(), os.Getenv("EVE_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if mode == "initialize" {
		return
	}
	w, err := s.LockWorkspace(os.Getenv("EVE_TEST_WORKSPACE"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	var probe Prober
	if mode == "crash" {
		probe = func(context.Context, int) error {
			fmt.Fprintln(os.Stdout, "candidate-committed")
			<-time.After(time.Minute) // Parent kills this process at this boundary.
			return context.DeadlineExceeded
		}
	}
	a, err := Reserve(t.Context(), w, probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(a); err != nil {
		t.Fatal(err)
	}
}

func worker(t *testing.T, root, id, mode string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAllocatorProcess$")
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	cmd.Env = append(os.Environ(), "EVE_TEST_ALLOCATOR="+mode, "EVE_TEST_STATE="+root, "EVE_TEST_WORKSPACE="+id)
	return cmd
}

func TestConcurrentFreshInitialization(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh")
	var commands []*exec.Cmd
	var inputs []io.WriteCloser
	var outputs []*bytes.Buffer
	for range 4 {
		cmd := worker(t, root, "", "initialize")
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output := new(bytes.Buffer)
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		inputs = append(inputs, input)
		outputs = append(outputs, output)
	}
	for _, input := range inputs {
		input.Close()
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("fresh initialization failed: %s %v", outputs[i], err)
		}
	}
	s, err := state.Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	register(t, s) // The winning schema must be usable, not merely present.
}

func TestConcurrentProcessesAcrossRepositories(t *testing.T) {
	s, root, r := storeFixture(t)
	repositories := []state.Repository{r, register(t, s)}
	type child struct {
		cmd    *exec.Cmd
		input  io.WriteCloser
		output *bytes.Buffer
	}
	var children []child
	for i := range 6 {
		w, id := intent(t, s, repositories[i%2], manifest, 20000, 49999)
		w.Close()
		cmd := worker(t, root, id, "allocate")
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output := new(bytes.Buffer)
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child{cmd, input, output})
	}
	for _, c := range children {
		c.input.Close()
	}
	claimed := map[int]bool{}
	for _, c := range children {
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("worker failed: %s %v", c.output, err)
		}
		var a state.Allocation
		if err := json.NewDecoder(c.output).Decode(&a); err != nil || !a.Ready {
			t.Fatalf("worker result: %+v %v", a, err)
		}
		for port := a.Base; port < a.Base+a.Size; port++ {
			if claimed[port] {
				t.Fatalf("concurrent processes overlapped on %d", port)
			}
			claimed[port] = true
		}
	}
	if len(claimed) != 6*4 {
		t.Fatal("not every port in every block was claimed")
	}
}

func TestCrashRetainsCandidateAndReleasesOSLock(t *testing.T) {
	s, root, r := storeFixture(t)
	w, id := intent(t, s, r, manifest, 20000, 49999)
	w.Close()
	cmd := worker(t, root, id, "crash")
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	marker := make([]byte, len("candidate-committed\n"))
	if _, err := io.ReadFull(output, marker); err != nil || string(marker) != "candidate-committed\n" {
		t.Fatalf("worker never reached durable boundary: %q %v", marker, err)
	}
	_, err = s.LockWorkspace(id)
	hasCode(t, err, "E_WORKSPACE_BUSY")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()

	pending, err := s.Allocation(t.Context(), id)
	if err != nil || pending.Size != 4 || pending.Ready {
		t.Fatalf("committed candidate lost after SIGKILL: %+v %v", pending, err)
	}
	resumer, err := s.LockWorkspace(id)
	if err != nil {
		t.Fatal("OS lock survived process death", err)
	}
	defer resumer.Close()
	same, err := resumer.Candidate(t.Context(), 0)
	if err != nil || !reflect.DeepEqual(pending, same) {
		t.Fatal("resume replaced the recorded candidate", err)
	}
	competing, _ := intent(t, s, r, manifest, 20000, 49999)
	other, err := Reserve(t.Context(), competing, nil)
	if err != nil || other.Base < pending.Base+pending.Size {
		t.Fatal("another allocator reused interrupted claims", err)
	}
	completed, err := Reserve(t.Context(), resumer, nil)
	if err != nil || !completed.Ready {
		t.Fatalf("resume failed: %+v %v", completed, err)
	}
}
