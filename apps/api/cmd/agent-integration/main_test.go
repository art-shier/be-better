package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"dayorder.local/api/internal/agentintegration"
)

func TestCommandHelpDoesNotLeakLegacyProviderKey(t *testing.T) {
	const canary = "task14-help-secret-canary"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestCommandProcess$", "--", "-h")
	command.Env = append(environmentWithout("DAYORDER_AGENT_PROVIDER_KEY"),
		"DAYORDER_AGENT_PROVIDER_KEY="+canary,
		"DAYORDER_AGENT_INTEGRATION_COMMAND_PROCESS=1",
	)

	output, runErr := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || !bytes.Contains(output, []byte("Usage of agent-integration:")) {
		t.Fatalf("command help did not execute: err=%v output=%s", runErr, output)
	}
	if bytes.Contains(output, []byte(canary)) {
		t.Fatalf("command help exposed the legacy Provider key: %s", output)
	}
}

func TestCommandProcess(t *testing.T) {
	if os.Getenv("DAYORDER_AGENT_INTEGRATION_COMMAND_PROCESS") != "1" {
		return
	}
	for index, argument := range os.Args {
		if argument == "--" {
			os.Args = append([]string{"agent-integration"}, os.Args[index+1:]...)
			os.Exit(run())
		}
	}
	os.Exit(3)
}

func TestParseCommandConfigFakeDoesNotReadProviderConfiguration(t *testing.T) {
	var lookups []string
	configuration, err := parseCommandConfig([]string{
		"--harness-origin", "http://127.0.0.1:5173",
	}, func(key string) (string, bool) {
		lookups = append(lookups, key)
		return "", false
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ProviderKey != "" || configuration.ProviderModel != "" {
		t.Fatalf("fake configuration received Provider settings: %#v", configuration)
	}
	for _, key := range lookups {
		if key == "DAYORDER_AGENT_TEST_PROVIDER_KEY" || key == "DAYORDER_AGENT_TEST_MODEL" ||
			key == "DAYORDER_AGENT_PROVIDER_KEY" || key == "DAYORDER_AGENT_PROVIDER_MODEL" {
			t.Fatalf("fake configuration read Provider environment %q", key)
		}
	}
}

func TestParseCommandConfigRealUsesDedicatedProviderEnvironment(t *testing.T) {
	values := map[string]string{
		"DAYORDER_AGENT_TEST_PROVIDER_KEY": "dedicated-provider-key",
		"DAYORDER_AGENT_TEST_MODEL":        "dedicated-model",
	}
	var lookups []string
	configuration, err := parseCommandConfig([]string{
		"--real-provider", "--harness-origin", "http://127.0.0.1:5173",
	}, func(key string) (string, bool) {
		lookups = append(lookups, key)
		value, ok := values[key]
		return value, ok
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.ProviderKey != "dedicated-provider-key" || configuration.ProviderModel != "dedicated-model" {
		t.Fatalf("real Provider configuration = %#v", configuration)
	}
	for _, legacy := range []string{"DAYORDER_AGENT_PROVIDER_KEY", "DAYORDER_AGENT_PROVIDER_MODEL"} {
		for _, key := range lookups {
			if key == legacy {
				t.Fatalf("real configuration read legacy Provider environment %q", legacy)
			}
		}
	}
}

func TestRunCommandHelpAndParseErrorsDoNotReadOrLeakProviderKey(t *testing.T) {
	const canary = "task14-dedicated-help-secret-canary"
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "help", args: []string{"-h"}},
		{name: "parse error", args: []string{"--unknown-option"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			var keyRead atomic.Bool
			code := runCommand(context.Background(), test.args, commandDependencies{
				stdin: strings.NewReader(""), stdout: io.Discard, stderr: &stderr,
				getenv: func(key string) (string, bool) {
					if key == "DAYORDER_AGENT_TEST_PROVIDER_KEY" {
						keyRead.Store(true)
						return canary, true
					}
					return "", false
				},
				start: func(context.Context, agentintegration.Config) (integrationHost, error) {
					t.Fatal("invalid invocation started the integration Host")
					return nil, errors.New("unreachable")
				},
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if keyRead.Load() || strings.Contains(stderr.String(), canary) {
				t.Fatalf("invalid invocation read or exposed the Provider key: %s", stderr.String())
			}
		})
	}
}

func TestRunCommandClosesOnStdinEOFAndEmitsOneReadyRecord(t *testing.T) {
	stdin, owner := io.Pipe()
	defer stdin.Close()
	host := &fakeIntegrationHost{apiURL: "http://127.0.0.1:48123"}
	var stdout, stderr bytes.Buffer
	result := make(chan int, 1)
	go func() {
		result <- runCommand(context.Background(), []string{
			"--harness-origin", "http://127.0.0.1:5173",
		}, commandDependencies{
			stdin: stdin, stdout: &stdout, stderr: &stderr,
			getenv: func(string) (string, bool) { return "", false },
			start:  func(context.Context, agentintegration.Config) (integrationHost, error) { return host, nil },
		})
	}()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("stdin EOF exit code = %d, stderr=%s", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		_ = stdin.CloseWithError(errors.New("release timed out scanner"))
		t.Fatal("stdin EOF did not stop the command")
	}
	if host.closes.Load() != 1 {
		t.Fatalf("Host.Close calls = %d, want 1", host.closes.Load())
	}
	lines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("stdout records = %d, want one: %s", len(lines), stdout.String())
	}
	var ready map[string]string
	if err := json.Unmarshal(lines[0], &ready); err != nil || ready["type"] != "ready" || ready["apiURL"] != host.apiURL {
		t.Fatalf("ready record = %q, err=%v", lines[0], err)
	}
}

func TestRunCommandReportsStdinFailureAfterCleanup(t *testing.T) {
	const canary = "task14-stdin-error-secret-canary"
	host := &fakeIntegrationHost{apiURL: "http://127.0.0.1:48123"}
	var stdout, stderr bytes.Buffer
	code := runCommand(context.Background(), []string{
		"--harness-origin", "http://127.0.0.1:5173",
	}, commandDependencies{
		stdin: iotest.ErrReader(errors.New(canary)), stdout: &stdout, stderr: &stderr,
		getenv: func(string) (string, bool) { return "", false },
		start:  func(context.Context, agentintegration.Config) (integrationHost, error) { return host, nil },
	})
	if code != 1 || host.closes.Load() != 1 || !strings.Contains(stderr.String(), "stdin failed") {
		t.Fatalf("stdin failure code=%d closes=%d stderr=%q", code, host.closes.Load(), stderr.String())
	}
	if strings.Contains(stdout.String(), canary) || strings.Contains(stderr.String(), canary) {
		t.Fatalf("stdin failure exposed the reader error: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunCommandReadyFailurePreservesCleanupDiagnostic(t *testing.T) {
	const fixtureName = "dayorder_agent_it_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	host := &fakeIntegrationHost{
		apiURL:   "http://127.0.0.1:48123",
		closeErr: errors.New("cleanup incomplete for temporary database " + fixtureName + ": synthetic close failure"),
	}
	var stderr bytes.Buffer
	code := runCommand(context.Background(), []string{
		"--harness-origin", "http://127.0.0.1:5173",
	}, commandDependencies{
		stdin: strings.NewReader(""), stdout: errorWriter{}, stderr: &stderr,
		getenv: func(string) (string, bool) { return "", false },
		start:  func(context.Context, agentintegration.Config) (integrationHost, error) { return host, nil },
	})
	if code != 1 || host.closes.Load() != 1 || !strings.Contains(stderr.String(), fixtureName) {
		t.Fatalf("ready failure code=%d closes=%d stderr=%q", code, host.closes.Load(), stderr.String())
	}
}

type fakeIntegrationHost struct {
	apiURL   string
	closeErr error
	closes   atomic.Int32
}

func (host *fakeIntegrationHost) APIURL() string { return host.apiURL }

func (host *fakeIntegrationHost) Close(context.Context) error {
	host.closes.Add(1)
	return host.closeErr
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("controlled output failure") }

func environmentWithout(key string) []string {
	prefix := strings.ToUpper(key) + "="
	values := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), prefix) {
			values = append(values, value)
		}
	}
	return values
}
