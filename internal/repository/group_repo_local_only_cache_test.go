package repository

import (
	"context"
	"errors"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
)

type localOnlySchedulerCacheStub struct {
	setErr   error
	setCalls int
	lastSet  *service.Group
}

func (c *localOnlySchedulerCacheStub) GetGroup(context.Context, int64) (*service.Group, error) {
	return nil, nil
}

func (c *localOnlySchedulerCacheStub) SetGroup(_ context.Context, group *service.Group) error {
	c.setCalls++
	c.lastSet = group
	return c.setErr
}

func (c *localOnlySchedulerCacheStub) DeleteGroup(context.Context, int64) error {
	return nil
}

type localOnlyFailingDriver struct {
	cache   *localOnlySchedulerCacheStub
	err     error
	called  bool
	ordered bool
}

func (d *localOnlyFailingDriver) Exec(context.Context, string, any, any) error {
	d.called = true
	d.ordered = d.cache != nil && d.cache.setCalls > 0
	return d.err
}

func (d *localOnlyFailingDriver) Query(context.Context, string, any, any) error {
	d.called = true
	d.ordered = d.cache != nil && d.cache.setCalls > 0
	return d.err
}

func (d *localOnlyFailingDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}

func (d *localOnlyFailingDriver) Close() error { return nil }

func (d *localOnlyFailingDriver) Dialect() string { return dialect.Postgres }

func newLocalOnlyUpdateTestRepo(cache service.SchedulerGroupCache, driver dialect.Driver) *groupRepository {
	client := dbent.NewClient(dbent.Driver(driver))
	return newGroupRepositoryWithSQLAndSchedulerCache(client, nil, cache)
}

func localOnlyUpdateTestGroup() *service.Group {
	return &service.Group{
		ID:               42,
		Name:             "local-only",
		Platform:         service.PlatformOpenAI,
		RateMultiplier:   1,
		LocalOnly:        true,
		Status:           service.StatusActive,
		SubscriptionType: service.SubscriptionTypeStandard,
	}
}

func TestGroupRepositoryUpdateLocalOnlyRejectsBeforeDatabaseWhenCacheWriteFails(t *testing.T) {
	cacheErr := errors.New("redis unavailable")
	cache := &localOnlySchedulerCacheStub{setErr: cacheErr}
	dbErr := errors.New("database must not be reached")
	driver := &localOnlyFailingDriver{cache: cache, err: dbErr}
	repo := newLocalOnlyUpdateTestRepo(cache, driver)

	err := repo.Update(context.Background(), localOnlyUpdateTestGroup())

	require.ErrorIs(t, err, cacheErr)
	require.Equal(t, 1, cache.setCalls)
	require.True(t, cache.lastSet.LocalOnly)
	require.False(t, driver.called, "cache failure must reject the update before any database write")
}

func TestGroupRepositoryUpdateLocalOnlyPrimesCacheBeforeDatabaseWrite(t *testing.T) {
	cache := &localOnlySchedulerCacheStub{}
	dbErr := errors.New("database write failed")
	driver := &localOnlyFailingDriver{cache: cache, err: dbErr}
	repo := newLocalOnlyUpdateTestRepo(cache, driver)

	err := repo.Update(context.Background(), localOnlyUpdateTestGroup())

	require.ErrorIs(t, err, dbErr)
	require.Equal(t, 1, cache.setCalls)
	require.True(t, driver.called)
	require.True(t, driver.ordered, "local-only deny state must reach the cache before the database write")
}

func TestGroupRepositoryUpdateLocalOnlyRejectsWhenSchedulerCacheUnavailable(t *testing.T) {
	dbErr := errors.New("database must not be reached")
	driver := &localOnlyFailingDriver{err: dbErr}
	repo := newLocalOnlyUpdateTestRepo(nil, driver)

	err := repo.Update(context.Background(), localOnlyUpdateTestGroup())

	require.EqualError(t, err, "scheduler group cache is required for local-only group update")
	require.False(t, driver.called, "missing security cache must fail closed before the database write")
}
