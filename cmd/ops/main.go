package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"signalwatch/internal/backfill"
	"signalwatch/internal/digest"
	"signalwatch/internal/platform/config"
	"signalwatch/internal/platform/db"
	"signalwatch/internal/platform/logging"
)

const serviceName = "signalwatch-ops-cli"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) error {
	task, id, err := parseRetryArguments(arguments)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger, err := logging.New(os.Stdout, serviceName, cfg.LogLevel)
	if err != nil {
		return err
	}
	database, err := db.Open(cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	retryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch task {
	case "digest":
		err = digest.NewMySQLDeliveryStore(database).Retry(retryCtx, id)
	case "backfill":
		err = backfill.NewMySQLStore(database).Retry(retryCtx, id)
	default:
		return errors.New("unknown retry task")
	}
	if err != nil {
		return err
	}
	logger.Info("failed task scheduled for retry", "task", arguments[1], "id", id)
	return nil
}

func parseRetryArguments(arguments []string) (string, uint64, error) {
	if len(arguments) < 2 || arguments[0] != "retry" {
		return "", 0, errors.New("usage: signalwatch-ops retry <digest|backfill> --id ID")
	}
	task := arguments[1]
	if task != "digest" && task != "backfill" {
		return "", 0, errors.New("unknown retry task")
	}
	flags := flag.NewFlagSet("retry", flag.ContinueOnError)
	id := flags.Uint64("id", 0, "delivery or subscription ID")
	if err := flags.Parse(arguments[2:]); err != nil {
		return "", 0, err
	}
	if *id == 0 || flags.NArg() != 0 {
		return "", 0, errors.New("a positive --id and no positional arguments are required")
	}
	return task, *id, nil
}
