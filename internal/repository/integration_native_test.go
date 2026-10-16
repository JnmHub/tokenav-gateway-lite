//go:build integration

package repository

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	redisclient "github.com/redis/go-redis/v9"
)

// Native tests mutate real rows. Only an explicitly named loopback test database
// is accepted, so a production DSN cannot silently replace the disposable container.
func initializeNativeIntegration(ctx context.Context) error {
	dsn := strings.TrimSpace(os.Getenv("TOKENAV_REPOSITORY_TEST_DATABASE_URL"))
	redisURL := strings.TrimSpace(os.Getenv("TOKENAV_TEST_REDIS_URL"))
	pg, err := url.Parse(dsn)
	if err != nil || pg == nil || (pg.Scheme != "postgres" && pg.Scheme != "postgresql") || !nativeTestLoopback(pg.Hostname()) || !strings.Contains(strings.ToLower(strings.TrimPrefix(pg.Path, "/")), "test") {
		return errors.New("TOKENAV_REPOSITORY_TEST_DATABASE_URL must name a loopback PostgreSQL test database")
	}
	redisTarget, err := url.Parse(redisURL)
	if err != nil || redisTarget == nil || !nativeTestLoopback(redisTarget.Hostname()) {
		return errors.New("TOKENAV_TEST_REDIS_URL must name the isolated loopback Redis service")
	}
	redisOptions, err := redisclient.ParseURL(redisURL)
	if err != nil {
		return errors.New("TOKENAV_TEST_REDIS_URL is invalid")
	}
	integrationDB, err = openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		return err
	}
	if err := ApplyMigrations(ctx, integrationDB); err != nil {
		_ = integrationDB.Close()
		return err
	}
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	integrationRedis = redisclient.NewClient(redisOptions)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := integrationRedis.Ping(pingCtx).Err(); err != nil {
		_ = integrationRedis.Close()
		_ = integrationEntClient.Close()
		return err
	}
	return nil
}

func nativeTestLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
