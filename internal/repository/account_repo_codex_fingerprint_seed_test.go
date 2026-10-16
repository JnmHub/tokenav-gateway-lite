package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestUpdateExtraEnsuresCodexFingerprintSeedAtomicallyWhenEnabling(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectExec(`(?s)UPDATE accounts SET extra = .*CASE WHEN platform = 'openai' AND type = 'oauth'.*jsonb_set.*gen_random_uuid\(\)::text.*WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(`{"codex_fingerprint_mode":"device"}`, int64(27)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	err = repo.UpdateExtra(context.Background(), 27, map[string]any{
		"codex_fingerprint_mode": "device",
		"codex_fingerprint_seed": "22222222-2222-4222-8222-222222222222",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateExtraPreservesExistingCanonicalSeedWhenEnabling(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectExec(`(?s)UPDATE accounts SET extra = .*to_jsonb\(extra ->> 'codex_fingerprint_seed'\).*WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(`{"codex_fingerprint_mode":"full"}`, int64(28)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	err = repo.UpdateExtra(context.Background(), 28, map[string]any{
		"codex_fingerprint_mode": "full",
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateExtraWithoutFingerprintModeKeepsPlainMerge(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	mock.ExpectExec(`(?s)UPDATE accounts SET extra = COALESCE\(extra, '{}'::jsonb\) \|\| \$1::jsonb, updated_at = NOW\(\) WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(`{"other_key":"v"}`, int64(29)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	err = repo.UpdateExtra(context.Background(), 29, map[string]any{"other_key": "v"})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdateExtraStripsClientProvidedSeed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })

	// 仅提交 seed 键：剥离后更新集为空，直接短路，不产生任何 SQL。
	repo := newAccountRepositoryWithSQL(client, nil, nil)
	require.NoError(t, repo.UpdateExtra(context.Background(), 30, map[string]any{
		"codex_fingerprint_seed": "22222222-2222-4222-8222-222222222222",
	}))
	require.NoError(t, mock.ExpectationsWereMet())

	// seed 键与其它键混提：payload 必须不含 seed。
	mock.ExpectExec(`(?s)UPDATE accounts SET extra = .*WHERE id = \$2 AND deleted_at IS NULL`).
		WithArgs(`{"codex_fingerprint_mode":"session"}`, int64(31)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpdateExtra(context.Background(), 31, map[string]any{
		"codex_fingerprint_mode": "session",
		"codex_fingerprint_seed": "22222222-2222-4222-8222-222222222222",
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureCodexFingerprintSeedSQLShapes(t *testing.T) {
	require.NotPanics(t, func() {
		expr := ensureCodexFingerprintSeedSQL("COALESCE(extra, '{}'::jsonb) || $1::jsonb")
		require.Contains(t, expr, "platform = 'openai' AND type = 'oauth'")
		require.Contains(t, expr, "jsonb_set")
		require.Contains(t, expr, "gen_random_uuid()::text")
		require.Contains(t, expr, "to_jsonb(extra ->> 'codex_fingerprint_seed')")
		require.Contains(t, expr, codexFingerprintSeedCanonicalPattern)
		require.Contains(t, expr, codexFingerprintNilSeed)
	})
	require.Contains(t, codexFingerprintSeedValidSQL("extra"), "codex_fingerprint_seed")
}

func TestStripCodexFingerprintSeedFromExtraUpdate(t *testing.T) {
	require.Nil(t, stripCodexFingerprintSeedFromExtraUpdate(nil))

	plain := map[string]any{"a": "b"}
	require.Equal(t, plain, stripCodexFingerprintSeedFromExtraUpdate(plain))

	stripped := stripCodexFingerprintSeedFromExtraUpdate(map[string]any{
		"a":                     "b",
		"codex_fingerprint_seed": "22222222-2222-4222-8222-222222222222",
	})
	require.Equal(t, map[string]any{"a": "b"}, stripped)

	only := stripCodexFingerprintSeedFromExtraUpdate(map[string]any{
		"codex_fingerprint_seed": "22222222-2222-4222-8222-222222222222",
	})
	require.Empty(t, only)
}
