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
	configPath := flag.String("config", "", "path to the JSON config file (required)")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "error: no configuration file provided; pass one with -config")
		flag.Usage()
		os.Exit(2)
	}

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
	cfg, err := internal.LoadConfig(configPath)
	if err != nil {
		return err
	}
	for _, warning := range cfg.Warnings {
		log.Warn("config: "+warning, "config", configPath)
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

	vars, err := internal.CurrentTemplateVars(hostID)
	if err != nil {
		log.Warn("could not determine primary IP/MAC address for templates", "error", err)
	}
	if cfg, err = internal.ResolveSubjects(cfg, vars); err != nil {
		return fmt.Errorf("invalid subjects in %s: %w", configPath, err)
	}

	if cfg, err = internal.RenderInputFiles(cfg, vars); err != nil {
		return fmt.Errorf("invalid input_files in %s: %w", configPath, err)
	}

	pathAts := make([]func(time.Time) (string, error), 0, len(cfg.OutputFiles))
	for _, file := range cfg.OutputFiles {
		tpl, err := internal.NewOutputPathTemplate(file, vars)
		if err != nil {
			return err
		}
		pathAts = append(pathAts, tpl.PathAt)
	}
	out, err := internal.OpenGzipJSONLs(pathAts...)
	if err != nil {
		return fmt.Errorf("invalid output_files in %s: %w", configPath, err)
	}
	defer out.Close()
	if err := internal.CheckInputOutputOverlap(cfg.InputFiles, out.Paths()); err != nil {
		return fmt.Errorf("invalid input_files in %s: %w", configPath, err)
	}

	return internal.NewAgent(cfg, hostID, out, log).Run(ctx)
}
