// Command agent watches a NATS subject for osquery SQL queries, runs them with osqueryi, and
// writes the results to another NATS subject and to a local gzip-compressed JSONL file.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whitfieldsdad/osquery-nats-sidecar/internal"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON config file (a default one is created if it does not exist)")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(*configPath, log); err != nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, log *slog.Logger) error {
	cfg, created, err := internal.LoadOrCreateConfig(configPath)
	if err != nil {
		return err
	}
	if created {
		log.Warn("no config file found; wrote a default one", "path", configPath)
	}
	internal.OsqueryBinary = cfg.OsqueryiPath

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Also confirms that osqueryi is installed and working before we start taking queries.
	idCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	hostID, err := internal.HostID(idCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("osqueryi (%s) is not usable: %w", cfg.OsqueryiPath, err)
	}

	out, err := internal.OpenGzipJSONL(cfg.OutputFile)
	if err != nil {
		return err
	}
	defer out.Close()

	return internal.NewAgent(cfg, hostID, out, log).Run(ctx)
}
