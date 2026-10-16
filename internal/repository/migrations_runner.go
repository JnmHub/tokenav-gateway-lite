package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
)

// schemaMigrationsTableDDL 定义迁移记录表的 DDL。
// 该表用于跟踪已应用的迁移文件及其校验和。
// - filename: 迁移文件名，作为主键唯一标识每个迁移
// - checksum: 文件内容的 SHA256 哈希值，用于检测迁移文件是否被篡改
// - applied_at: 迁移应用时间戳
const schemaMigrationsTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   TEXT PRIMARY KEY,
	checksum   TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

const atlasSchemaRevisionsTableDDL = `
CREATE TABLE IF NOT EXISTS atlas_schema_revisions (
	version TEXT PRIMARY KEY,
	description TEXT NOT NULL,
	type INTEGER NOT NULL,
	applied INTEGER NOT NULL DEFAULT 0,
	total INTEGER NOT NULL DEFAULT 0,
	executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	execution_time BIGINT NOT NULL DEFAULT 0,
	error TEXT NULL,
	error_stmt TEXT NULL,
	hash TEXT NOT NULL DEFAULT '',
	partial_hashes TEXT[] NULL,
	operator_version TEXT NULL
);
`

// migrationsAdvisoryLockID 是用于序列化迁移操作的 PostgreSQL Advisory Lock ID。
// 在多实例部署场景下，该锁确保同一时间只有一个实例执行迁移。
// 任何稳定的 int64 值都可以，只要不与同一数据库中的其他锁冲突即可。
const migrationsAdvisoryLockID int64 = 694208311321144027
const migrationsLockRetryInterval = 500 * time.Millisecond
const migrationsUnlockTimeout = 5 * time.Second
const nonTransactionalMigrationSuffix = "_notx.sql"
const paymentOrdersOutTradeNoUniqueMigration = "120_enforce_payment_orders_out_trade_no_unique_notx.sql"
const paymentOrdersOutTradeNoUniqueIndex = "paymentorder_out_trade_no_unique"
const schedulerOutboxPendingDedupKeyMigration = "153_scheduler_outbox_pending_dedup_key_index_notx.sql"
const schedulerOutboxPendingDedupKeyIndex = "idx_scheduler_outbox_pending_dedup_key"
const accountsDuplicateOperationIDUniqueMigration = "165_accounts_duplicate_operation_id_unique_notx.sql"
const accountsDuplicateOperationIDUniqueIndex = "idx_accounts_duplicate_operation_id_unique"
const usageLogsEffectiveModelIndexesMigration = "223_add_usage_log_effective_model_indexes_notx.sql"
const usageLogsEffectiveRequestedModelIndex = "idx_usage_logs_effective_requested_model_created"
const usageLogsEffectiveUpstreamModelIndex = "idx_usage_logs_effective_upstream_model_created"

// migrationSession 同时由 *sql.DB 和固定的 *sql.Conn 实现。
// 真正执行迁移时必须使用 *sql.Conn，确保 session advisory lock（会话级咨询锁）
// 的获取、全部迁移语句和释放都发生在同一个 PostgreSQL session（会话）。
type migrationSession interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

type migrationChecksumCompatibilityRule struct {
	fileChecksum       string
	acceptedDBChecksum map[string]struct{}
	acceptedChecksums  map[string]struct{}
}

// migrationChecksumCompatibilityRules 仅用于兼容历史上误修改过的迁移文件 checksum。
// 规则必须同时匹配「迁移名 + 数据库 checksum + 当前文件 checksum」且两者都落在该迁移的已知版本集合内才会放行，
// 避免放宽全局校验，也允许将误改的历史 migration 回滚为已发布版本而不要求人工修 checksum。
var migrationChecksumCompatibilityRules = map[string]migrationChecksumCompatibilityRule{
	"054_drop_legacy_cache_columns.sql":                       newMigrationChecksumCompatibilityRule("82de761156e03876653e7a6a4eee883cd927847036f779b0b9f34c42a8af7a7d", "182c193f3359946cf094090cd9e57d5c3fd9abaffbc1e8fc378646b8a6fa12b4"),
	"061_add_usage_log_request_type.sql":                      newMigrationChecksumCompatibilityRule("66207e7aa5dd0429c2e2c0fabdaf79783ff157fa0af2e81adff2ee03790ec65c", "08a248652cbab7cfde147fc6ef8cda464f2477674e20b718312faa252e0481c0", "222b4a09c797c22e5922b6b172327c824f5463aaa8760e4f621bc5c22e2be0f3"),
	// 101/107 只调整了说明注释；客户节点和美国节点已核验仍使用原 SQL 校验值，
	// 兼容这两个已知版本，避免无结构变化的发布因历史 checksum 被拒绝启动。
	"101_add_account_stats_pricing.sql":                    newMigrationChecksumCompatibilityRule("3debd12202be6760d0154465da1c8ca68474c05b047464e5ef782b119bb4f2f1", "a442674cb9b22aacf1879995d23c73792cfb1bd0fb1d0d04f396cda2880d4cc2"),
	"107_add_account_cost_to_dashboard_tables.sql":          newMigrationChecksumCompatibilityRule("f8f7292406211d429bc4dbd32db9ed2c30336aaea947eada420ac75a17fd87ef", "8742f876b999808fc9f478fd2db06037b26d9ab74b10e872517211ee7805d663"),
	"109_auth_identity_compat_backfill.sql":                   newMigrationChecksumCompatibilityRule("0580b4602d85435edf9aca1633db580bb3932f26517f75134106f80275ec2ace", "551e498aa5616d2d91096e9d72cf9fb36e418ee22eacc557f8811cadbc9e20ee"),
	"110_pending_auth_and_provider_default_grants.sql":        newMigrationChecksumCompatibilityRule("32cf87ee787b1bb36b5c691367c96eee37518fa3eed6f3322cf68795e3745279", "e3d1f433be2b564cfbdc549adf98fce13c5c7b363ebc20fd05b765d0563b0925"),
	"112_add_payment_order_provider_key_snapshot.sql":         newMigrationChecksumCompatibilityRule("b75f8f56d39455682787696a3d92ad25b055444ca328fb7fca9a460a15d68d99", "ffd3e8a2c9295fa9cbefefd629a78268877e5b51bc970a82d9b3f46ec4ebd15e"),
	"115_auth_identity_legacy_external_backfill.sql":          newMigrationChecksumCompatibilityRule("022aadd97bb53e755f0cf7a3a957e0cb1a1353b0c39ec4de3234acd2871fd04f", "4cf39e508be9fd1a5aa41610cbbebeb80385c9adda45bf78a706de9db4f1385f"),
	"116_auth_identity_legacy_external_safety_reports.sql":    newMigrationChecksumCompatibilityRule("07edb09fa8d04ffb172b0621e3c22f4d1757d20a24ae267b3b36b087ab72d488", "f7757bd929ac67ffb08ce69fa4cf20fad39dbff9d5a5085fb2adabb7607e5877"),
	"118_wechat_dual_mode_and_auth_source_defaults.sql":       newMigrationChecksumCompatibilityRule("b54194d7a3e4fbf710e0a3590d22a2fe7966804c487052a356e0b55f53ef96b0", "e0cdf835d6c688d64100f483d31bc02ac9ebad414bf1837af239a84bf75b8227", "a38243ca0a72c3a01c0a92b7986423054d6133c0399441f853b99802852720fb"),
	"119_enforce_payment_orders_out_trade_no_unique.sql":      newMigrationChecksumCompatibilityRule("0bbe809ae48a9d811dabda1ba1c74955bd71c4a9cc610f9128816818dfa6c11e", "ebd2c67cce0116393fb4f1b5d5116a67c6aceb73820dfb5133d1ff6f36d72d34"),
	"120_enforce_payment_orders_out_trade_no_unique_notx.sql": newMigrationChecksumCompatibilityRule("34aadc0db59a4e390f92a12b73bd74642d9724f33124f73638ae00089ea5e074", "e77921f79d539bc24575cb9c16cbe566d2b23ce816190343d0a7568f6a3fcf61", "707431450603e70a43ce9fbd61e0c12fa67da4875158ccefabacea069587ab22", "04b082b5a239c525154fe9185d324ee2b05ff90da9297e10dba19f9be79aa59a"),
	"123_fix_legacy_auth_source_grant_on_signup_defaults.sql": newMigrationChecksumCompatibilityRule("2ce43c2cd89e9f9e1febd34a407ed9e84d177386c5544b6f02c1f58a21129f57", "6cd33422f215dcd1f486ab6f35c0ea5805d9ca69bb25906d94bc649156657145"),
	// 该迁移曾以相同文件名、不同注释版本发布到日本节点；现场约束已包含 grok，
	// 因此只兼容已核验的旧 checksum，不能通过修改 schema_migrations 绕过全局校验。
	"157_user_platform_quotas_add_grok.sql": newMigrationChecksumCompatibilityRule("ac19eda91f1881c3d4adb3249b274d3d7575140760ce0d962bc78b3a7e09f27d", "5cace8fa32c6174a72721cd9b01f28f4545de1fd7bcd9ca196a4225056ec4fb8"),
	// 189 由上游的 `ADD COLUMN allow_live`（非幂等）改为 `IF NOT EXISTS`：
	// 本仓库自有的 180_add_group_live_and_usage_type.sql 会先建出同一列，导致全新空库
	// 迁移失败。只登记已发布的旧 checksum，保证已迁移节点仍可通过启动校验。
	"189_add_group_allow_live.sql": newMigrationChecksumCompatibilityRule("0b1be3ea162fc41c5760f82b0b8c8d64f088a74b251921c8dee79a34a7e6b99d", "51172b10c160e7f560346dbaf736dc8e92feb793cd00169f5fb876c399460862"),
	// 220 增加了 `ALTER COLUMN video_model_prices DROP NOT NULL` 前置语句：
	// 本仓库自有的 210_group_grok_media_prices.sql 把该列建成了 NOT NULL DEFAULT '{}'，
	// 与 220 清空非 Grok 分组视频价的 UPDATE 冲突，全新空库迁移必然中断。
	"220_clear_non_grok_video_generation_config.sql": newMigrationChecksumCompatibilityRule("2368e54dead2590a946575ee2d5f0a0d162a38857490961a34b68c2a2d143751", "cf4dbfa75ac27d93a30a6a14439fe7dccfc911c043358363d5ec47946aa0e28b"),
}

// ApplyMigrations 将嵌入的 SQL 迁移文件应用到指定的数据库。
//
// 该函数可以在每次应用启动时安全调用：
// - 已应用的迁移会被自动跳过（通过校验 filename 判断）
// - 如果迁移文件内容被修改（checksum 不匹配），会返回错误
// - 使用 PostgreSQL Advisory Lock 确保多实例并发安全
//
// 参数：
//   - ctx: 上下文，用于超时控制和取消
//   - db: 数据库连接
//
// 返回：
//   - error: 迁移过程中的任何错误
func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("nil sql db")
	}
	return applyMigrationsFS(ctx, db, migrations.FS)
}

// applyMigrationsFS 是迁移执行的核心实现。
// 它从指定的文件系统读取 SQL 迁移文件并按顺序应用。
//
// 迁移执行流程：
//  1. 获取 PostgreSQL Advisory Lock，防止多实例并发迁移
//  2. 确保 schema_migrations 表存在
//  3. 按文件名排序读取所有 .sql 文件
//  4. 对于每个迁移文件：
//     - 计算文件内容的 SHA256 校验和
//     - 检查该迁移是否已应用（通过 filename 查询）
//     - 如果已应用，验证校验和是否匹配
//     - 如果未应用，在事务中执行迁移并记录
//  5. 释放 Advisory Lock
//
// 参数：
//   - ctx: 上下文
//   - db: 数据库连接
//   - fsys: 包含迁移文件的文件系统（通常是 embed.FS）
func applyMigrationsFS(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	if db == nil {
		return errors.New("nil sql db")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin migrations connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	return applyMigrationsSession(ctx, conn, fsys)
}

func applyMigrationsSession(ctx context.Context, db migrationSession, fsys fs.FS) error {

	// 获取分布式锁，确保多实例部署时只有一个实例执行迁移。
	// 这是 PostgreSQL 特有的 Advisory Lock 机制。
	if err := pgAdvisoryLock(ctx, db); err != nil {
		return err
	}
	defer func() {
		// 无论迁移是否成功，都要释放锁。
		// 使用 context.Background() 确保即使原 ctx 已取消也能释放锁。
		unlockCtx, cancel := context.WithTimeout(context.Background(), migrationsUnlockTimeout)
		defer cancel()
		_ = pgAdvisoryUnlock(unlockCtx, db)
	}()

	// 创建迁移记录表（如果不存在）。
	// 该表记录所有已应用的迁移及其校验和。
	if _, err := db.ExecContext(ctx, schemaMigrationsTableDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// 自动对齐 Atlas 基线（如果检测到 legacy schema_migrations 且缺失 atlas_schema_revisions）。
	if err := ensureAtlasBaselineAligned(ctx, db, fsys); err != nil {
		return err
	}

	// 获取所有 .sql 迁移文件并按文件名排序。
	// 命名规范：使用零填充数字前缀（如 001_init.sql, 002_add_users.sql）。
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files) // 确保按文件名顺序执行迁移

	for _, name := range files {
		// 读取迁移文件内容
		contentBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		// Git 在 Windows clean worktree 中可能把 LF 自动转为 CRLF，而 embed 会原样打包这些字节。
		// 执行和计算 checksum 前统一恢复为 LF，避免同一份迁移因构建平台不同被误判为篡改。
		// 只规范 CRLF 行尾，文件内其他任何字节变化仍会改变 checksum。
		content := normalizeMigrationContent(string(contentBytes))
		if content == "" {
			continue // 跳过空文件
		}

		// 计算文件内容的 SHA256 校验和，用于检测文件是否被修改。
		// 这是一种防篡改机制：如果有人修改了已应用的迁移文件，系统会拒绝启动。
		checksum := migrationContentChecksum(content)

		// 检查该迁移是否已经应用
		var existing string
		rowErr := db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE filename = $1", name).Scan(&existing)
		if rowErr == nil {
			// 迁移已应用，验证校验和是否匹配
			if existing != checksum {
				// 兼容特定历史误改场景（仅白名单规则），其余仍保持严格不可变约束。
				if isMigrationChecksumCompatible(name, existing, checksum) {
					continue
				}
				// 校验和不匹配意味着迁移文件在应用后被修改，这是危险的。
				// 正确的做法是创建新的迁移文件来进行变更。
				return fmt.Errorf(
					"migration %s checksum mismatch (db=%s file=%s)\n"+
						"This means the migration file was modified after being applied to the database.\n"+
						"Solutions:\n"+
						"  1. Revert to original: git log --oneline -- migrations/%s && git checkout <commit> -- migrations/%s\n"+
						"  2. For new changes, create a new migration file instead of modifying existing ones\n"+
						"Note: Modifying applied migrations breaks the immutability principle and can cause inconsistencies across environments",
					name, existing, checksum, name, name,
				)
			}
			continue // 迁移已应用且校验和匹配，跳过
		}
		if !errors.Is(rowErr, sql.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", name, rowErr)
		}

		nonTx, err := validateMigrationExecutionMode(name, content)
		if err != nil {
			return fmt.Errorf("validate migration %s: %w", name, err)
		}

		if nonTx {
			if err := prepareNonTransactionalMigration(ctx, db, name); err != nil {
				return fmt.Errorf("prepare migration %s: %w", name, err)
			}

			// *_notx.sql：用于 CREATE/DROP INDEX CONCURRENTLY 场景，必须非事务执行。
			// 逐条语句执行，避免将多条 CONCURRENTLY 语句放入同一个隐式事务块。
			statements := splitSQLStatements(content)
			for i, stmt := range statements {
				trimmed := strings.TrimSpace(stmt)
				if trimmed == "" {
					continue
				}
				if stripSQLLineComment(trimmed) == "" {
					continue
				}
				if _, err := db.ExecContext(ctx, trimmed); err != nil {
					return fmt.Errorf("apply migration %s (non-tx statement %d): %w", name, i+1, err)
				}
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)", name, checksum); err != nil {
				return fmt.Errorf("record migration %s (non-tx): %w", name, err)
			}
			continue
		}

		// 默认迁移在事务中执行，确保原子性：要么完全成功，要么完全回滚。
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}

		// 执行迁移 SQL
		if _, err := tx.ExecContext(ctx, content); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		// 记录迁移已完成，保存文件名和校验和
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)", name, checksum); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}

		// 提交事务
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}

func prepareNonTransactionalMigration(ctx context.Context, db migrationSession, name string) error {
	switch name {
	case paymentOrdersOutTradeNoUniqueMigration:
		return preparePaymentOrdersOutTradeNoUniqueMigration(ctx, db)
	case schedulerOutboxPendingDedupKeyMigration:
		return dropInvalidIndexIfPresent(ctx, db, schedulerOutboxPendingDedupKeyIndex)
	case accountsDuplicateOperationIDUniqueMigration:
		return prepareAccountsDuplicateOperationIDUniqueMigration(ctx, db)
	case usageLogsEffectiveModelIndexesMigration:
		// CREATE INDEX CONCURRENTLY 若曾被中断，会留下同名但 invalid 的索引；
		// IF NOT EXISTS 无法修复它，因此迁移重试前必须先清理。
		for _, indexName := range []string{usageLogsEffectiveRequestedModelIndex, usageLogsEffectiveUpstreamModelIndex} {
			if err := dropInvalidIndexIfPresent(ctx, db, indexName); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func preparePaymentOrdersOutTradeNoUniqueMigration(ctx context.Context, db migrationSession) error {
	duplicates, err := findDuplicatePaymentOrderOutTradeNos(ctx, db)
	if err != nil {
		return fmt.Errorf("precheck duplicate out_trade_no: %w", err)
	}
	if len(duplicates) > 0 {
		return fmt.Errorf(
			"duplicate out_trade_no values block %s; remediate duplicates before retrying: %s",
			paymentOrdersOutTradeNoUniqueMigration,
			strings.Join(duplicates, ", "),
		)
	}

	return dropInvalidIndexIfPresent(ctx, db, paymentOrdersOutTradeNoUniqueIndex)
}

func prepareAccountsDuplicateOperationIDUniqueMigration(ctx context.Context, db migrationSession) error {
	duplicates, err := findDuplicateAccountOperationIDs(ctx, db)
	if err != nil {
		return fmt.Errorf("precheck duplicate account operation ids: %w", err)
	}
	if len(duplicates) > 0 {
		return fmt.Errorf(
			"duplicate account operation ids block %s; inspect and remediate duplicates before retrying: %s",
			accountsDuplicateOperationIDUniqueMigration,
			strings.Join(duplicates, ", "),
		)
	}

	state, err := inspectAccountsDuplicateOperationIDIndex(ctx, db)
	if err != nil {
		return fmt.Errorf("inspect account duplicate operation index: %w", err)
	}
	if !state.exists {
		return nil
	}
	if !state.valid {
		// CREATE UNIQUE INDEX CONCURRENTLY 失败会留下同名 INVALID 索引；
		// 必须先清理，否则 IF NOT EXISTS 会跳过重建并让幂等保护静默失效。
		if _, err := db.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+accountsDuplicateOperationIDUniqueIndex); err != nil {
			return fmt.Errorf("drop invalid index %s: %w", accountsDuplicateOperationIDUniqueIndex, err)
		}
		return nil
	}
	if !state.unique || state.keyColumns != 1 || normalizeIndexCatalogExpression(state.expression) != "extra->>'duplicate_operation_id'" ||
		normalizeIndexCatalogExpression(state.predicate) != "deleted_atisnullandnullifbtrimextra->>'duplicate_operation_id',''isnotnull" {
		return fmt.Errorf(
			"existing index %s has an unexpected definition; refusing to let IF NOT EXISTS silently skip the required unique constraint",
			accountsDuplicateOperationIDUniqueIndex,
		)
	}
	return nil
}

type accountsDuplicateOperationIndexState struct {
	exists     bool
	valid      bool
	unique     bool
	keyColumns int
	expression string
	predicate  string
}

func inspectAccountsDuplicateOperationIDIndex(ctx context.Context, db migrationSession) (accountsDuplicateOperationIndexState, error) {
	var state accountsDuplicateOperationIndexState
	err := db.QueryRowContext(ctx, `
		SELECT i.indisvalid,
		       i.indisunique,
		       i.indnkeyatts,
		       COALESCE(pg_get_expr(i.indexprs, i.indrelid), ''),
		       COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
		FROM pg_class idx
		JOIN pg_namespace ns ON ns.oid = idx.relnamespace
		JOIN pg_index i ON i.indexrelid = idx.oid
		JOIN pg_class tbl ON tbl.oid = i.indrelid
		WHERE ns.nspname = 'public'
		  AND idx.relname = $1
		  AND tbl.relname = 'accounts'
	`, accountsDuplicateOperationIDUniqueIndex).Scan(&state.valid, &state.unique, &state.keyColumns, &state.expression, &state.predicate)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.exists = true
	return state, nil
}

func normalizeIndexCatalogExpression(value string) string {
	replacer := strings.NewReplacer(
		" ", "",
		"\t", "",
		"\r", "",
		"\n", "",
		"(", "",
		")", "",
		"::text", "",
		"\"", "",
	)
	return strings.ToLower(replacer.Replace(value))
}

func dropInvalidIndexIfPresent(ctx context.Context, db migrationSession, indexName string) error {
	invalid, err := indexIsInvalid(ctx, db, indexName)
	if err != nil {
		return fmt.Errorf("check invalid index %s: %w", indexName, err)
	}
	if !invalid {
		return nil
	}

	if _, err := db.ExecContext(ctx, fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s", indexName)); err != nil {
		return fmt.Errorf("drop invalid index %s: %w", indexName, err)
	}
	return nil
}

func findDuplicatePaymentOrderOutTradeNos(ctx context.Context, db migrationSession) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT out_trade_no, COUNT(*) AS duplicate_count
		FROM payment_orders
		WHERE out_trade_no <> ''
		GROUP BY out_trade_no
		HAVING COUNT(*) > 1
		ORDER BY duplicate_count DESC, out_trade_no
		LIMIT 5
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	duplicates := make([]string, 0, 5)
	for rows.Next() {
		var outTradeNo string
		var duplicateCount int
		if err := rows.Scan(&outTradeNo, &duplicateCount); err != nil {
			return nil, err
		}
		duplicates = append(duplicates, fmt.Sprintf("%s (count=%d)", outTradeNo, duplicateCount))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return duplicates, nil
}

func findDuplicateAccountOperationIDs(ctx context.Context, db migrationSession) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		WITH active_operations AS (
			SELECT id, btrim(extra ->> 'duplicate_operation_id') AS operation_id
			FROM accounts
			WHERE deleted_at IS NULL
			  AND NULLIF(btrim(extra ->> 'duplicate_operation_id'), '') IS NOT NULL
		)
		SELECT left(operation_id, 16) AS operation_prefix,
		       COUNT(*) AS duplicate_count,
		       array_to_string(array_agg(id ORDER BY id), ',') AS account_ids
		FROM active_operations
		GROUP BY operation_id
		HAVING COUNT(*) > 1
		ORDER BY duplicate_count DESC, operation_prefix
		LIMIT 5
	`)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	duplicates := make([]string, 0, 5)
	for rows.Next() {
		var operationPrefix string
		var duplicateCount int
		var accountIDs string
		if err := rows.Scan(&operationPrefix, &duplicateCount, &accountIDs); err != nil {
			return nil, err
		}
		duplicates = append(duplicates, fmt.Sprintf("%s... (count=%d account_ids=%s)", operationPrefix, duplicateCount, accountIDs))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return duplicates, nil
}

func indexIsInvalid(ctx context.Context, db migrationSession, indexName string) (bool, error) {
	var invalid bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_class idx
			JOIN pg_namespace ns ON ns.oid = idx.relnamespace
			JOIN pg_index i ON i.indexrelid = idx.oid
			WHERE ns.nspname = 'public'
			  AND idx.relname = $1
			  AND NOT i.indisvalid
		)
	`, indexName).Scan(&invalid)
	return invalid, err
}

func ensureAtlasBaselineAligned(ctx context.Context, db migrationSession, fsys fs.FS) error {
	hasLegacy, err := tableExists(ctx, db, "schema_migrations")
	if err != nil {
		return fmt.Errorf("check schema_migrations: %w", err)
	}
	if !hasLegacy {
		return nil
	}

	hasAtlas, err := tableExists(ctx, db, "atlas_schema_revisions")
	if err != nil {
		return fmt.Errorf("check atlas_schema_revisions: %w", err)
	}
	if !hasAtlas {
		if _, err := db.ExecContext(ctx, atlasSchemaRevisionsTableDDL); err != nil {
			return fmt.Errorf("create atlas_schema_revisions: %w", err)
		}
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM atlas_schema_revisions").Scan(&count); err != nil {
		return fmt.Errorf("count atlas_schema_revisions: %w", err)
	}
	if count > 0 {
		return nil
	}

	version, description, hash, err := latestMigrationBaseline(fsys)
	if err != nil {
		return fmt.Errorf("atlas baseline version: %w", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO atlas_schema_revisions (version, description, type, applied, total, executed_at, execution_time, hash)
		VALUES ($1, $2, $3, 0, 0, NOW(), 0, $4)
	`, version, description, 1, hash); err != nil {
		return fmt.Errorf("insert atlas baseline: %w", err)
	}
	return nil
}

func tableExists(ctx context.Context, db migrationSession, tableName string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)
	`, tableName).Scan(&exists)
	return exists, err
}

func latestMigrationBaseline(fsys fs.FS) (string, string, string, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return "", "", "", err
	}
	if len(files) == 0 {
		return "baseline", "baseline", "", nil
	}
	sort.Strings(files)
	name := files[len(files)-1]
	contentBytes, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", "", "", err
	}
	content := normalizeMigrationContent(string(contentBytes))
	hash := migrationContentChecksum(content)
	version := strings.TrimSuffix(name, ".sql")
	return version, version, hash, nil
}

// normalizeMigrationContent 将迁移文件的平台行尾统一为 LF。
// 不处理独立 CR、行内空白或 BOM，以免扩大防篡改校验的等价范围。
func normalizeMigrationContent(content string) string {
	return strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
}

func migrationContentChecksum(content string) string {
	sum := sha256.Sum256([]byte(normalizeMigrationContent(content)))
	return hex.EncodeToString(sum[:])
}

func checksumSet(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func newMigrationChecksumCompatibilityRule(fileChecksum string, acceptedDBChecksums ...string) migrationChecksumCompatibilityRule {
	return migrationChecksumCompatibilityRule{
		fileChecksum:       fileChecksum,
		acceptedDBChecksum: checksumSet(acceptedDBChecksums...),
		acceptedChecksums:  checksumSet(append([]string{fileChecksum}, acceptedDBChecksums...)...),
	}
}

func isMigrationChecksumCompatible(name, dbChecksum, fileChecksum string) bool {
	rule, ok := migrationChecksumCompatibilityRules[name]
	if !ok {
		return false
	}
	_, dbOK := rule.acceptedChecksums[dbChecksum]
	if !dbOK {
		return false
	}
	_, fileOK := rule.acceptedChecksums[fileChecksum]
	return fileOK
}

func validateMigrationExecutionMode(name, content string) (bool, error) {
	normalizedName := strings.ToLower(strings.TrimSpace(name))
	upperContent := strings.ToUpper(content)
	nonTx := strings.HasSuffix(normalizedName, nonTransactionalMigrationSuffix)

	if !nonTx {
		if strings.Contains(upperContent, "CONCURRENTLY") {
			return false, errors.New("CONCURRENTLY statements must be placed in *_notx.sql migrations")
		}
		return false, nil
	}

	if strings.Contains(upperContent, "BEGIN") || strings.Contains(upperContent, "COMMIT") || strings.Contains(upperContent, "ROLLBACK") {
		return false, errors.New("*_notx.sql must not contain transaction control statements (BEGIN/COMMIT/ROLLBACK)")
	}

	statements := splitSQLStatements(content)
	for _, stmt := range statements {
		normalizedStmt := strings.ToUpper(stripSQLLineComment(strings.TrimSpace(stmt)))
		if normalizedStmt == "" {
			continue
		}

		if strings.Contains(normalizedStmt, "CONCURRENTLY") {
			isCreateIndex := strings.Contains(normalizedStmt, "CREATE") && strings.Contains(normalizedStmt, "INDEX")
			isDropIndex := strings.Contains(normalizedStmt, "DROP") && strings.Contains(normalizedStmt, "INDEX")
			if !isCreateIndex && !isDropIndex {
				return false, errors.New("*_notx.sql currently only supports CREATE/DROP INDEX CONCURRENTLY statements")
			}
			if isCreateIndex && !strings.Contains(normalizedStmt, "IF NOT EXISTS") {
				return false, errors.New("CREATE INDEX CONCURRENTLY in *_notx.sql must include IF NOT EXISTS for idempotency")
			}
			if isDropIndex && !strings.Contains(normalizedStmt, "IF EXISTS") {
				return false, errors.New("DROP INDEX CONCURRENTLY in *_notx.sql must include IF EXISTS for idempotency")
			}
			continue
		}

		return false, errors.New("*_notx.sql must not mix non-CONCURRENTLY SQL statements")
	}

	return true, nil
}

func splitSQLStatements(content string) []string {
	parts := strings.Split(content, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func stripSQLLineComment(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// pgAdvisoryLock 获取 PostgreSQL Advisory Lock。
// Advisory Lock 是一种轻量级的锁机制，不与任何特定的数据库对象关联。
// 它非常适合用于应用层面的分布式锁场景，如迁移序列化。
func pgAdvisoryLock(ctx context.Context, db migrationSession) error {
	ticker := time.NewTicker(migrationsLockRetryInterval)
	defer ticker.Stop()

	for {
		var locked bool
		if err := db.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationsAdvisoryLockID).Scan(&locked); err != nil {
			return fmt.Errorf("acquire migrations lock: %w", err)
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acquire migrations lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// pgAdvisoryUnlock 释放 PostgreSQL Advisory Lock。
// 必须在获取锁后确保释放，否则会阻塞其他实例的迁移操作。
func pgAdvisoryUnlock(ctx context.Context, db migrationSession) error {
	_, err := db.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationsAdvisoryLockID)
	if err != nil {
		return fmt.Errorf("release migrations lock: %w", err)
	}
	return nil
}
