package app

import (
	"testing"
	"time"
)

func TestLoadConfigBackupDefaults(t *testing.T) {
	t.Setenv("BACKUP_DIR", "")
	t.Setenv("BACKUP_INTERVAL", "")
	t.Setenv("BACKUP_RETENTION", "")

	cfg := LoadConfig()
	if cfg.BackupDir != "data/backups" {
		t.Fatalf("BackupDir = %q, want data/backups", cfg.BackupDir)
	}
	if cfg.BackupInterval != 24*time.Hour {
		t.Fatalf("BackupInterval = %v, want 24h", cfg.BackupInterval)
	}
	if cfg.BackupRetention != 7 {
		t.Fatalf("BackupRetention = %d, want 7", cfg.BackupRetention)
	}
}

func TestLoadConfigReadsAndValidatesBackupSettings(t *testing.T) {
	t.Setenv("BACKUP_DIR", "custom backups")
	t.Setenv("BACKUP_INTERVAL", "36h")
	t.Setenv("BACKUP_RETENTION", "12")

	cfg := LoadConfig()
	if cfg.BackupDir != "custom backups" || cfg.BackupInterval != 36*time.Hour || cfg.BackupRetention != 12 {
		t.Fatalf("backup config = dir %q interval %v retention %d", cfg.BackupDir, cfg.BackupInterval, cfg.BackupRetention)
	}

	t.Setenv("BACKUP_INTERVAL", "0")
	t.Setenv("BACKUP_RETENTION", "-2")
	cfg = LoadConfig()
	if cfg.BackupInterval != defaultBackupInterval || cfg.BackupRetention != defaultBackupRetention {
		t.Fatalf("invalid backup config did not fall back: interval %v retention %d", cfg.BackupInterval, cfg.BackupRetention)
	}
}
