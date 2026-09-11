//go:build linux

package m0

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type shellHarness struct {
	base, binary, bashScript, zshScript string
}

func completionShellHarness(t *testing.T) shellHarness {
	t.Helper()
	base := t.TempDir()
	binary := filepath.Join(base, "bin", "eve")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/eve")
	build.Dir = module
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	harness := shellHarness{base: base, binary: binary, bashScript: filepath.Join(base, "eve.bash"), zshScript: filepath.Join(base, "_eve")}
	for _, generated := range []struct{ shell, path string }{{"bash", harness.bashScript}, {"zsh", harness.zshScript}} {
		data, err := exec.Command(binary, "completion", generated.shell).CombinedOutput()
		if err != nil {
			t.Fatalf("generate %s: %v %s", generated.shell, err, data)
		}
		if strings.Contains(string(data), "out=$(eval") || strings.Contains(string(data), "eval _describe") {
			t.Fatalf("%s executed a re-evaluated completion request", generated.shell)
		}
		if err := os.WriteFile(generated.path, data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return harness
}

type ptyStep struct {
	Text    string
	Pause   time.Duration
	WaitFor string
	Timeout time.Duration
}

type ptyTranscript struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (p *ptyTranscript) Write(value []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data.Write(value)
}

func (p *ptyTranscript) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data.String()
}

func interactivePTY(t *testing.T, shellExe string, shellArgs []string, scriptPath string, steps []ptyStep, binaryDir string) string {
	t.Helper()
	scriptExe, err := exec.LookPath("script")
	if err != nil {
		t.Skip("util-linux script is unavailable")
	}
	command := exec.Command(scriptExe, "-qefc", shellExe+" "+strings.Join(shellArgs, " "), scriptPath)
	command.Env = append(os.Environ(), "PATH="+binaryDir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	output := &ptyTranscript{}
	command.Stdout = output
	command.Stderr = output
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if _, err := stdin.Write([]byte(step.Text)); err != nil {
			t.Fatalf("PTY input %q: %v", step.Text, err)
		}
		if step.WaitFor != "" {
			deadline := time.After(step.Timeout)
			for !strings.Contains(output.String(), step.WaitFor) {
				select {
				case <-deadline:
					t.Fatalf("PTY did not reach %q:\n%s", step.WaitFor, output.String())
				case <-time.After(25 * time.Millisecond):
				}
			}
		}
		if step.Pause != 0 {
			time.Sleep(step.Pause)
		}
	}
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatal("interactive shell test timed out")
	}
	return output.String()
}

func staticInsertionSteps(base, sourceScript, bashCompletion string) (marker string, steps []ptyStep) {
	marker = filepath.Join(base, "marker")
	steps = []ptyStep{
		{Text: sourceScript + "\n", Pause: 400 * time.Millisecond},
		{Text: "eve completion ba\t", WaitFor: "eve completion bash", Timeout: 10 * time.Second},
		{Text: "\003", Pause: 250 * time.Millisecond},
		{Text: "eve create 'literal$(touch " + marker + ")'\t", Pause: 1500 * time.Millisecond},
		{Text: "\003", Pause: 250 * time.Millisecond},
		{Text: "exit\n"},
	}
	if bashCompletion != "" {
		steps = append([]ptyStep{{Text: "source " + bashCompletion + "\n", Pause: 400 * time.Millisecond}}, steps...)
	}
	return marker, steps
}

func TestRealBashCompletionInsertion(t *testing.T) {
	if _, err := os.Stat("/usr/share/bash-completion/bash_completion"); err != nil {
		t.Skip("bash-completion helpers are unavailable")
	}
	harness := completionShellHarness(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	marker, steps := staticInsertionSteps(harness.base, "source "+harness.bashScript, "/usr/share/bash-completion/bash_completion")
	output := interactivePTY(t, bash, []string{"--noprofile", "--norc", "-i"}, filepath.Join(harness.base, "typescript"), steps, filepath.Dir(harness.binary))
	if !strings.Contains(output, "eve completion bash") {
		t.Fatalf("Bash did not insert the exact completion:\n%s", output)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("Bash completion evaluated typed text:\n%s", output)
	}
}

func TestRealZshCompletionInsertion(t *testing.T) {
	harness := completionShellHarness(t)
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is unavailable")
	}
	setup := "autoload -Uz compinit\ncompinit -i\nsource " + harness.zshScript
	marker, steps := staticInsertionSteps(harness.base, setup, "")
	output := interactivePTY(t, zsh, []string{"-f", "-i"}, filepath.Join(harness.base, "typescript"), steps, filepath.Dir(harness.binary))
	if !strings.Contains(output, "eve completion bash") {
		t.Fatalf("Zsh did not insert the exact completion:\n%s", output)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("Zsh completion evaluated typed text:\n%s", output)
	}
}

func TestCompletionIsInertWhenEveDisappears(t *testing.T) {
	harness := completionShellHarness(t)
	if err := os.Remove(harness.binary); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, exe  string
		args       []string
		setup      string
		completion string
	}{
		{"bash", "/usr/bin/bash", []string{"--noprofile", "--norc", "-i"}, "source /usr/share/bash-completion/bash_completion\nsource " + harness.bashScript, "eve completion ba\t"},
		{"zsh", "/usr/bin/zsh", []string{"-f", "-i"}, "autoload -Uz compinit\ncompinit -i\nsource " + harness.zshScript, "eve completion ba\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.exe); err != nil {
				t.Skip(tc.exe + " is unavailable")
			}
			steps := []ptyStep{
				{Text: tc.setup + "\n", Pause: 400 * time.Millisecond},
				{Text: tc.completion, Pause: 1500 * time.Millisecond},
				{Text: "\003", Pause: 250 * time.Millisecond},
				{Text: "exit\n"},
			}
			output := interactivePTY(t, tc.exe, tc.args, filepath.Join(harness.base, "typescript-"+tc.name), steps, filepath.Dir(harness.binary))
			if strings.Contains(output, "eve completion bash") {
				t.Fatalf("completion used a missing EVE executable:\n%s", output)
			}
		})
	}
}
