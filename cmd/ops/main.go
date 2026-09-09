package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

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
	if len(arguments) < 2 || arguments[0] != "role" {
		return errors.New("usage: signalwatch-ops role <grant|revoke|list> [--email address]")
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
