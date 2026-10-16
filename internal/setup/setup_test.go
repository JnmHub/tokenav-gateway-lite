package setup

import (
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestDecideAdminBootstrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		totalUsers int64
		adminUsers int64
		should     bool
		reason     string
	}{
		{
			name:       "empty database should create admin",
			totalUsers: 0,
			adminUsers: 0,
			should:     true,
			reason:     adminBootstrapReasonEmptyDatabase,
		},
		{
			name:       "admin exists should skip",
			totalUsers: 10,
			adminUsers: 1,
			should:     false,
			reason:     adminBootstrapReasonAdminExists,
		},
		{
			name:       "users exist without admin should skip",
			totalUsers: 5,
			adminUsers: 0,
			should:     false,
			reason:     adminBootstrapReasonUsersExistWithoutAdmin,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decideAdminBootstrap(tc.totalUsers, tc.adminUsers)
			if got.shouldCreate != tc.should {
				t.Fatalf("shouldCreate=%v, want %v", got.shouldCreate, tc.should)
			}
			if got.reason != tc.reason {
				t.Fatalf("reason=%q, want %q", got.reason, tc.reason)
			}
		})
	}
}

func TestSetupDefaultAdminConcurrency(t *testing.T) {
	t.Run("simple mode admin uses higher concurrency", func(t *testing.T) {
		t.Setenv("RUN_MODE", "simple")
		if got := setupDefaultAdminConcurrency(); got != simpleModeAdminConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, simpleModeAdminConcurrency)
		}
	})

	t.Run("standard mode keeps existing default", func(t *testing.T) {
		t.Setenv("RUN_MODE", "standard")
		if got := setupDefaultAdminConcurrency(); got != defaultUserConcurrency {
			t.Fatalf("setupDefaultAdminConcurrency()=%d, want %d", got, defaultUserConcurrency)
		}
	})
}

func TestWriteConfigFileKeepsDefaultUserConcurrency(t *testing.T) {
	t.Setenv("RUN_MODE", "simple")
	t.Setenv("DATA_DIR", t.TempDir())

	if err := writeConfigFile(&SetupConfig{}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}

	data, err := os.ReadFile(GetConfigFilePath())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !strings.Contains(string(data), "user_concurrency: 5") {
		t.Fatalf("config missing default user concurrency, got:\n%s", string(data))
	}
}

func TestWriteConfigFileCreatesDataDir(t *testing.T) {
	dataDir := t.TempDir() + string(os.PathSeparator) + "nested" + string(os.PathSeparator) + "data"
	t.Setenv("DATA_DIR", dataDir)

	if err := writeConfigFile(&SetupConfig{}); err != nil {
		t.Fatalf("writeConfigFile() error = %v", err)
	}
	if _, err := os.Stat(GetConfigFilePath()); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
}

func TestBuildDatabaseConnectionDSNsUsesPostgresForBootstrap(t *testing.T) {
	cfg := &DatabaseConfig{
		Host:     "db",
		Port:     5432,
		User:     "sub2api",
		Password: "secret",
		DBName:   "sub2api",
		SSLMode:  "disable",
	}

	bootstrapDSN, targetDSN := buildDatabaseConnectionDSNs(cfg)

	if !strings.Contains(bootstrapDSN, "dbname=postgres") {
		t.Fatalf("bootstrap DSN = %q, want default postgres database", bootstrapDSN)
	}
	if strings.Contains(bootstrapDSN, "dbname=sub2api") {
		t.Fatalf("bootstrap DSN = %q, should not connect to target database before checking/creating it", bootstrapDSN)
	}
	if !strings.Contains(targetDSN, "dbname=sub2api") {
		t.Fatalf("target DSN = %q, want configured database", targetDSN)
	}
}

func TestSetupConfigFromRuntimeConfigDoesNotEmbedAdminDefaults(t *testing.T) {
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Host:     "db",
			Port:     5432,
			User:     "sub2api",
			Password: "secret",
			DBName:   "sub2api",
			SSLMode:  "disable",
		},
		RunMode:  config.RunModeGatewayLite,
		Timezone: "Asia/Shanghai",
	}

	got := setupConfigFromRuntimeConfig(cfg, "", "")

	if got.Admin.Email != "" {
		t.Fatalf("admin email=%q, want empty bootstrap credential", got.Admin.Email)
	}
	if got.Admin.Password != "" {
		t.Fatalf("admin password should not have a compiled default")
	}
	if got.Database.Host != cfg.Database.Host || got.Database.DBName != cfg.Database.DBName {
		t.Fatalf("database config not copied: %+v", got.Database)
	}
	if got.RunMode != config.RunModeGatewayLite {
		t.Fatalf("run mode=%q, want %q", got.RunMode, config.RunModeGatewayLite)
	}
}

func TestSetupConfigFromRuntimeConfigOverridesAdmin(t *testing.T) {
	cfg := &config.Config{}

	got := setupConfigFromRuntimeConfig(cfg, " admin@example.com ", "custom-password")

	if got.Admin.Email != "admin@example.com" {
		t.Fatalf("admin email=%q, want trimmed override", got.Admin.Email)
	}
	if got.Admin.Password != "custom-password" {
		t.Fatalf("admin password override not applied")
	}
}
