package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"dayorder.local/api/internal/agentintegration"
	"dayorder.local/api/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	processCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	return runCommand(processCtx, os.Args[1:], commandDependencies{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.LookupEnv,
		start: func(ctx context.Context, configuration agentintegration.Config) (integrationHost, error) {
			return agentintegration.Start(ctx, configuration)
		},
	})
}

type integrationHost interface {
	APIURL() string
	Close(context.Context) error
}

type commandDependencies struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) (string, bool)
	start  func(context.Context, agentintegration.Config) (integrationHost, error)
}

func parseCommandConfig(args []string, getenv func(string) (string, bool), stderr io.Writer) (agentintegration.Config, error) {
	flags := flag.NewFlagSet("agent-integration", flag.ContinueOnError)
	flags.SetOutput(stderr)
	environment := flags.String("environment", valueOrLookup(getenv, "DAYORDER_AGENT_INTEGRATION_ENV", string(config.Test)), "development or test")
	realProvider := flags.Bool("real-provider", false, "use the real DeepSeek Provider")
	providerModel := flags.String("provider-model", "", "test Provider model")
	harnessOrigin := flags.String("harness-origin", "", "explicit loopback HTTP origin for the browser harness")
	if err := flags.Parse(args); err != nil {
		return agentintegration.Config{}, err
	}
	if strings.TrimSpace(*harnessOrigin) == "" {
		return agentintegration.Config{}, errors.New("--harness-origin is required")
	}
	configuration := agentintegration.Config{
		Environment: config.Environment(*environment), RealProvider: *realProvider,
		AllowedOrigins: []string{*harnessOrigin},
	}
	if *realProvider {
		configuration.ProviderKey = valueOrLookup(getenv, "DAYORDER_AGENT_TEST_PROVIDER_KEY", "")
		configuration.ProviderModel = strings.TrimSpace(*providerModel)
		if configuration.ProviderModel == "" {
			configuration.ProviderModel = valueOrLookup(getenv, "DAYORDER_AGENT_TEST_MODEL", "")
		}
	}
	return configuration, nil
}

func runCommand(ctx context.Context, args []string, dependencies commandDependencies) int {
	configuration, err := parseCommandConfig(args, dependencies.getenv, dependencies.stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(dependencies.stderr, "agent integration host:", err)
		}
		return 2
	}
	host, err := dependencies.start(ctx, configuration)
	if err != nil {
		fmt.Fprintln(dependencies.stderr, "agent integration host start failed:", err)
		return 1
	}
	if err = json.NewEncoder(dependencies.stdout).Encode(map[string]string{"type": "ready", "apiURL": host.APIURL()}); err != nil {
		fmt.Fprintln(dependencies.stderr, "agent integration ready output failed")
		if cleanupErr := closeHost(host); cleanupErr != nil {
			fmt.Fprintln(dependencies.stderr, "agent integration host cleanup failed:", cleanupErr)
		}
		return 1
	}

	stopInput := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(dependencies.stdin)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "stop" {
				stopInput <- nil
				return
			}
		}
		stopInput <- scanner.Err()
	}()
	var inputErr error
	select {
	case <-ctx.Done():
	case inputErr = <-stopInput:
	}
	if inputErr != nil {
		fmt.Fprintln(dependencies.stderr, "agent integration stdin failed")
	}
	if err = closeHost(host); err != nil {
		fmt.Fprintln(dependencies.stderr, "agent integration host cleanup failed:", err)
		return 1
	}
	if inputErr != nil {
		return 1
	}
	return 0
}

func closeHost(host integrationHost) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return host.Close(ctx)
}

func valueOrLookup(getenv func(string) (string, bool), key, fallback string) string {
	if getenv != nil {
		value, _ := getenv(key)
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return fallback
}
