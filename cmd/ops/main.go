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
	"signalwatch/internal/operator"
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
	if len(arguments) < 2 || (arguments[0] != "role" && arguments[0] != "retry") {
		return errors.New("usage: signalwatch-ops role <grant|revoke|list> [--email address] | retry <digest|backfill> --id ID")
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
	if arguments[0] == "retry" {
		flags := flag.NewFlagSet("retry", flag.ContinueOnError)
		id := flags.Uint64("id", 0, "delivery or subscription ID")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		if *id == 0 {
			return errors.New("--id is required")
		}
		retryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		switch arguments[1] {
		case "digest":
			err = digest.NewMySQLDeliveryStore(database).Retry(retryCtx, *id)
		case "backfill":
			err = backfill.NewMySQLStore(database).Retry(retryCtx, *id)
		default:
			return errors.New("unknown retry task")
		}
		if err != nil {
			return err
		}
		logger.Info("failed task scheduled for retry", "task", arguments[1], "id", *id)
		return nil
	}
	service, err := operator.NewService(database, time.Now)
	if err != nil {
		return err
	}
	switch arguments[1] {
	case "grant", "revoke":
		flags := flag.NewFlagSet(arguments[1], flag.ContinueOnError)
		email := flags.String("email", "", "existing account email")
		if err := flags.Parse(arguments[2:]); err != nil {
			return err
		}
		if *email == "" {
			return errors.New("--email is required")
		}
		var account operator.Account
		if arguments[1] == "grant" {
			account, err = service.Grant(ctx, *email)
		} else {
			account, err = service.Revoke(ctx, *email)
		}
		if err != nil {
			return err
		}
		event := "operator_role_granted"
		if arguments[1] == "revoke" {
			event = "operator_role_revoked"
		}
		logger.Info("operator role changed", "module", "operations", "event", event,
			"account_id", account.ID, "account_email", account.Email,
			"account_status", account.Status, "role", account.Role)
		return nil
	case "list":
		accounts, err := service.List(ctx)
		if err != nil {
			return err
		}
		for _, account := range accounts {
			logger.Info("operator account", "module", "operations", "event", "operator_role_listed",
				"account_id", account.ID, "account_email", account.Email,
				"account_status", account.Status, "role", account.Role, "updated_at", account.UpdatedAt)
		}
		return nil
	default:
		return errors.New("unknown role command")
	}
}
