package agentcmd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/tg-monitor/tg-monitor/internal/agentapp"
	"github.com/tg-monitor/tg-monitor/internal/agentclient"
	"github.com/tg-monitor/tg-monitor/internal/config"
	"github.com/tg-monitor/tg-monitor/internal/linuxmetrics"
)

type Runner interface {
	Run(context.Context) error
	Once(context.Context) error
}

type Dependencies struct {
	Stdout      io.Writer
	Stderr      io.Writer
	LoadConfig  func() (config.AgentConfig, error)
	BuildRunner func(config.AgentConfig, *slog.Logger) (Runner, error)
}

func Run(ctx context.Context, args []string, dependencies Dependencies) error {
	if len(args) != 1 {
		return errors.New("usage: tg-monitor-agent <run|once>")
	}
	mode := args[0]
	if mode != "run" && mode != "once" {
		return errors.New("unknown command; usage: tg-monitor-agent <run|once>")
	}
	dependencies = dependencies.withDefaults()
	cfg, err := dependencies.LoadConfig()
	if err != nil {
		return safeCommandError{"load agent configuration", err}
	}
	logger := slog.New(slog.NewJSONHandler(dependencies.Stderr, nil))
	runner, err := dependencies.BuildRunner(cfg, logger)
	if err != nil {
		return safeCommandError{"build agent runtime", err}
	}
	if mode == "run" {
		if err := runner.Run(ctx); err != nil {
			return safeCommandError{"run agent", err}
		}
		return nil
	}
	if err := runner.Once(ctx); err != nil {
		return safeCommandError{"run one agent report", err}
	}
	return nil
}

func (dependencies Dependencies) withDefaults() Dependencies {
	if dependencies.Stdout == nil {
		dependencies.Stdout = os.Stdout
	}
	if dependencies.Stderr == nil {
		dependencies.Stderr = os.Stderr
	}
	if dependencies.LoadConfig == nil {
		dependencies.LoadConfig = config.LoadAgentFromEnv
	}
	if dependencies.BuildRunner == nil {
		dependencies.BuildRunner = buildRunner
	}
	return dependencies
}

func buildRunner(cfg config.AgentConfig, logger *slog.Logger) (Runner, error) {
	sender, err := agentclient.New(cfg.EndpointURL, cfg.Token, cfg.HTTPTimeout)
	if err != nil {
		return nil, err
	}
	return agentapp.New(linuxmetrics.NewCollector(), sender, cfg.Interval, logger), nil
}

type safeCommandError struct {
	stage string
	cause error
}

func (err safeCommandError) Error() string { return err.stage + " failed" }
func (err safeCommandError) Unwrap() error { return err.cause }
