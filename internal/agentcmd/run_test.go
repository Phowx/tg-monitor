package agentcmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tg-monitor/tg-monitor/internal/config"
)

const commandCanaryToken = "command-canary-agent-token"

type runnerStub struct {
	runCalls  int
	onceCalls int
	runErr    error
	onceErr   error
}

func (runner *runnerStub) Run(context.Context) error {
	runner.runCalls++
	return runner.runErr
}

func (runner *runnerStub) Once(context.Context) error {
	runner.onceCalls++
	return runner.onceErr
}

type commandHarness struct {
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	config     config.AgentConfig
	loadCalls  int
	buildCalls int
	builtWith  config.AgentConfig
	logger     *slog.Logger
	runner     *runnerStub
	loadErr    error
	buildErr   error
}

func newCommandHarness() *commandHarness {
	return &commandHarness{
		config: config.AgentConfig{
			EndpointURL: "https://monitor.example/api/v1/metrics",
			Token:       commandCanaryToken,
			Interval:    15 * time.Second,
			HTTPTimeout: 10 * time.Second,
		},
		runner: &runnerStub{},
	}
}

func (harness *commandHarness) dependencies() Dependencies {
	return Dependencies{
		Stdout: &harness.stdout,
		Stderr: &harness.stderr,
		LoadConfig: func() (config.AgentConfig, error) {
			harness.loadCalls++
			return harness.config, harness.loadErr
		},
		BuildRunner: func(cfg config.AgentConfig, logger *slog.Logger) (Runner, error) {
			harness.buildCalls++
			harness.builtWith = cfg
			harness.logger = logger
			return harness.runner, harness.buildErr
		},
	}
}

func TestRunRejectsInvalidArgumentsBeforeDependencies(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"unknown"}, {"run", "extra"}, {"once", "extra"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			harness := newCommandHarness()
			if err := Run(context.Background(), args, harness.dependencies()); err == nil {
				t.Fatalf("Run(%v) error = nil", args)
			}
			if harness.loadCalls != 0 || harness.buildCalls != 0 || harness.runner.runCalls != 0 || harness.runner.onceCalls != 0 {
				t.Fatalf("invalid command invoked dependencies: %#v", harness)
			}
			assertCommandSafe(t, nil, harness.stderr.String())
		})
	}
}

func TestRunDispatchesRunWithExactConfigAndJSONLogger(t *testing.T) {
	harness := newCommandHarness()
	if err := Run(context.Background(), []string{"run"}, harness.dependencies()); err != nil {
		t.Fatalf("Run(run) error = %v", err)
	}
	if harness.loadCalls != 1 || harness.buildCalls != 1 || harness.builtWith != harness.config || harness.logger == nil {
		t.Fatalf("load/build/config/logger = %d/%d/%#v/%v", harness.loadCalls, harness.buildCalls, harness.builtWith, harness.logger)
	}
	if harness.runner.runCalls != 1 || harness.runner.onceCalls != 0 {
		t.Fatalf("runner calls = run %d once %d", harness.runner.runCalls, harness.runner.onceCalls)
	}
	harness.logger.Info("logger-shape-check", "mode", "run")
	if output := harness.stderr.String(); !strings.Contains(output, `"msg":"logger-shape-check"`) || !strings.Contains(output, `"mode":"run"`) {
		t.Fatalf("stderr is not JSON slog output: %q", output)
	}
	assertCommandSafe(t, nil, harness.stderr.String())
}

func TestRunDispatchesOnceOnly(t *testing.T) {
	harness := newCommandHarness()
	if err := Run(context.Background(), []string{"once"}, harness.dependencies()); err != nil {
		t.Fatalf("Run(once) error = %v", err)
	}
	if harness.runner.onceCalls != 1 || harness.runner.runCalls != 0 {
		t.Fatalf("runner calls = once %d run %d", harness.runner.onceCalls, harness.runner.runCalls)
	}
}

func TestRunPropagatesErrorsWithoutRenderingSecrets(t *testing.T) {
	for _, stage := range []string{"config", "build", "run", "once"} {
		t.Run(stage, func(t *testing.T) {
			harness := newCommandHarness()
			sentinel := errors.New(stage + " failed with " + commandCanaryToken)
			switch stage {
			case "config":
				harness.loadErr = sentinel
			case "build":
				harness.buildErr = sentinel
			case "run":
				harness.runner.runErr = sentinel
			case "once":
				harness.runner.onceErr = sentinel
			}
			command := stage
			if command == "config" || command == "build" {
				command = "run"
			}
			err := Run(context.Background(), []string{command}, harness.dependencies())
			if !errors.Is(err, sentinel) {
				t.Fatalf("Run() error = %v, want sentinel", err)
			}
			assertCommandSafe(t, err, harness.stderr.String())
		})
	}
}

func assertCommandSafe(t *testing.T, err error, stderr string) {
	t.Helper()
	for _, text := range []string{stderr, errorText(err)} {
		for _, forbidden := range []string{commandCanaryToken, "Authorization"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("command output leaked %q: %q", forbidden, text)
			}
		}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
