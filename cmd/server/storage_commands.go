package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"pikpak2directlink/internal/app"
)

func runStorageCommand(args []string, cfg app.Config) (bool, error) {
	return runStorageCommandWithOutput(args, cfg, os.Stdout)
}

func runStorageCommandWithOutput(args []string, cfg app.Config, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "storage" {
		return false, nil
	}
	if len(args) < 2 {
		return true, errors.New("storage command requires restore-db or restore-migration")
	}

	switch args[1] {
	case "restore-db":
		backup, err := parseRestoreFlags("storage restore-db", args[2:])
		if err != nil {
			return true, err
		}
		result, err := app.RestoreDatabase(context.Background(), cfg.DBFile, backup)
		if err != nil {
			return true, err
		}
		printRestoreResult(output, result)
		return true, nil
	case "restore-migration":
		backup, err := parseRestoreFlags("storage restore-migration", args[2:])
		if err != nil {
			return true, err
		}
		result, err := app.RestoreMigration(context.Background(), cfg, backup)
		if err != nil {
			return true, err
		}
		printRestoreResult(output, result)
		return true, nil
	default:
		return true, fmt.Errorf("unknown storage command %q", args[1])
	}
}

func parseRestoreFlags(name string, args []string) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	backup := flags.String("backup", "", "backup file or directory")
	confirmed := flags.Bool("yes", false, "confirm destructive restore")
	if err := flags.Parse(args); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("%s: unexpected arguments: %s", name, strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*backup) == "" {
		return "", fmt.Errorf("%s requires --backup", name)
	}
	if !*confirmed {
		return "", fmt.Errorf("%s requires --yes because restore replaces current data", name)
	}
	return *backup, nil
}

func printRestoreResult(output io.Writer, result app.StorageRestoreResult) {
	fmt.Fprintf(output, "storage restore completed; restored %d file(s)\n", len(result.RestoredPaths))
	if result.SafetyBackupPath != "" {
		fmt.Fprintf(output, "pre-restore safety backup: %s\n", result.SafetyBackupPath)
	}
}
