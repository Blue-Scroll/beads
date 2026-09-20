package dolt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
)

// TestPoolTimeoutSixtySecondsReachesDriverDSN pins the town's own case for
// gastownhall/beads#6144 (Blue Scroll bead hq-d9ip4). The bd CLI opens its
// store through New, and New runs applyConfigDefaults first, so a 60s override
// must show up in the DSN that buildServerDSN hands the mysql driver. The DSN is
// parsed back instead of printed, so a password from the environment can never
// land in test output.
func TestPoolTimeoutSixtySecondsReachesDriverDSN(t *testing.T) {
	driverTimeouts := func(t *testing.T, cfg *Config) (time.Duration, time.Duration) {
		t.Helper()
		parsed, err := mysql.ParseDSN(buildServerDSN(cfg, cfg.Database))
		if err != nil {
			t.Fatalf("driver could not parse the DSN: %v", err)
		}
		return parsed.ReadTimeout, parsed.WriteTimeout
	}

	t.Run("env vars set to 60s reach the driver on the New path", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "60s")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "60s")
		cfg := &Config{ServerMode: true, Database: "hq", Path: t.TempDir()}

		applyConfigDefaults(cfg) // the first thing New(ctx, cfg) does

		if cfg.PoolReadTimeout != 60*time.Second || cfg.PoolWriteTimeout != 60*time.Second {
			t.Fatalf("pool timeouts = %v/%v, want 60s/60s from the env vars", cfg.PoolReadTimeout, cfg.PoolWriteTimeout)
		}
		read, write := driverTimeouts(t, cfg)
		if read != 60*time.Second || write != 60*time.Second {
			t.Fatalf("driver readTimeout/writeTimeout = %v/%v, want 60s/60s", read, write)
		}
	})

	t.Run("config.yaml dolt block reaches the driver with no config.Initialize", func(t *testing.T) {
		// This is the route a library caller like gc takes: it never calls
		// config.Initialize, so the ladder must fall back to reading
		// <BeadsDir>/config.yaml directly.
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		config.ResetForTesting()
		t.Cleanup(config.ResetForTesting)
		beadsDir := t.TempDir()
		yaml := "dolt:\n  pool-read-timeout: 60s\n  pool-write-timeout: 60s\n"
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{ServerMode: true, Database: "hq", Path: t.TempDir(), BeadsDir: beadsDir}

		applyConfigDefaults(cfg)

		if cfg.PoolReadTimeout != 60*time.Second || cfg.PoolWriteTimeout != 60*time.Second {
			t.Fatalf("pool timeouts = %v/%v, want 60s/60s from config.yaml", cfg.PoolReadTimeout, cfg.PoolWriteTimeout)
		}
		read, write := driverTimeouts(t, cfg)
		if read != 60*time.Second || write != 60*time.Second {
			t.Fatalf("driver readTimeout/writeTimeout = %v/%v, want 60s/60s", read, write)
		}
	})

	t.Run("nothing set keeps the built-in 10s", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_POOL_READ_TIMEOUT", "")
		t.Setenv("BEADS_DOLT_POOL_WRITE_TIMEOUT", "")
		config.ResetForTesting()
		t.Cleanup(config.ResetForTesting)
		cfg := &Config{ServerMode: true, Database: "hq", Path: t.TempDir(), BeadsDir: t.TempDir()}

		applyConfigDefaults(cfg)

		read, write := driverTimeouts(t, cfg)
		if read != 10*time.Second || write != 10*time.Second {
			t.Fatalf("driver readTimeout/writeTimeout = %v/%v, want the 10s defaults", read, write)
		}
	})
}
