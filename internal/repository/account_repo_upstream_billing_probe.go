package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListDueUpstreamBillingProbeAccountIDs 返回一小批到期账号。unsupported 在账号
// 配置未变化时不会重复轰炸；任何身份配置编辑都会由服务层删除旧快照后重新入队。
func (r *accountRepository) ListDueUpstreamBillingProbeAccountIDs(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	if limit < 1 {
		return []int64{}, nil
	}
	if limit > 20 {
		limit = 20
	}
	rows, err := r.client.QueryContext(ctx, `
		SELECT id
		FROM accounts
		WHERE deleted_at IS NULL
			AND platform = 'openai'
			AND type = 'apikey'
			AND status = 'active'
			AND extra->>'upstream_provider' = 'sub2api'
			AND extra @> '{"upstream_billing_auto_probe_enabled": true}'::jsonb
			AND COALESCE(extra#>>'{upstream_billing_probe,status}', '') <> 'unsupported'
			AND (
				extra->'upstream_billing_probe' IS NULL
				OR COALESCE(extra#>>'{upstream_billing_probe,next_probe_at}', '') = ''
				OR CASE
					WHEN extra#>>'{upstream_billing_probe,next_probe_at}' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
						THEN (extra#>>'{upstream_billing_probe,next_probe_at}')::timestamptz <= $1
					ELSE TRUE
				END
			)
		ORDER BY COALESCE(extra#>>'{upstream_billing_probe,next_probe_at}', ''), id
		LIMIT $2
	`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// UpdateUpstreamBillingAutoProbeEnabled 定向修改账号级开关，不允许覆盖其它 extra 字段。
// updated_at CAS 防止与管理员同时编辑账号时互相吞掉配置。
func (r *accountRepository) UpdateUpstreamBillingAutoProbeEnabled(ctx context.Context, account *service.Account, enabled bool) error {
	if account == nil || account.UpdatedAt.IsZero() {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	client := clientFromContext(ctx, r.client)
	var query string
	if enabled {
		query = `
			UPDATE accounts
			SET extra = jsonb_set(COALESCE(extra, '{}'::jsonb), '{upstream_billing_auto_probe_enabled}', 'true'::jsonb, true), updated_at = NOW()
			WHERE id = $1 AND updated_at = $2 AND platform = 'openai' AND type = 'apikey'
				AND extra->>'upstream_provider' = 'sub2api' AND deleted_at IS NULL
		`
	} else {
		query = `
			UPDATE accounts
			SET extra = COALESCE(extra, '{}'::jsonb) - 'upstream_billing_auto_probe_enabled', updated_at = NOW()
			WHERE id = $1 AND updated_at = $2 AND platform = 'openai' AND type = 'apikey'
				AND extra->>'upstream_provider' = 'sub2api' AND deleted_at IS NULL
		`
	}
	result, err := client.ExecContext(ctx, query, account.ID, account.UpdatedAt)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	r.syncSchedulerAccountSnapshot(ctx, account.ID)
	return nil
}

// UpdateUpstreamBillingProbeSnapshot 只合并观测快照，不改账号手工成本倍率和调度状态。
// CAS（比较并交换）以读取时 updated_at 和代理身份为门禁，慢探测不得覆盖管理员的新配置。
func (r *accountRepository) UpdateUpstreamBillingProbeSnapshot(
	ctx context.Context,
	account *service.Account,
	snapshot *service.UpstreamBillingProbeSnapshot,
) error {
	if account == nil || snapshot == nil {
		return service.ErrAccountNilInput
	}
	if dbent.TxFromContext(ctx) != nil {
		return r.updateUpstreamBillingProbeSnapshotInTx(ctx, account, snapshot)
	}
	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		return r.updateUpstreamBillingProbeSnapshotInTx(ctx, account, snapshot)
	}
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := r.updateUpstreamBillingProbeSnapshotInTx(txCtx, account, snapshot); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// P0 只展示快照，不发 scheduler outbox（调度事件）；
	// 定向同步单账号只是避免本机管理查询看到旧 extra。
	r.syncSchedulerAccountSnapshot(ctx, account.ID)
	return nil
}

func (r *accountRepository) updateUpstreamBillingProbeSnapshotInTx(
	ctx context.Context,
	account *service.Account,
	snapshot *service.UpstreamBillingProbeSnapshot,
) error {
	if account.UpdatedAt.IsZero() {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	proxyMatches, err := lockAndMatchUpstreamBillingProbeProxy(ctx, clientFromContext(ctx, r.client), account)
	if err != nil {
		return err
	}
	if !proxyMatches {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	payload, err := json.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot})
	if err != nil {
		return err
	}
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, `
		UPDATE accounts
		SET extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb, updated_at = NOW()
		WHERE id = $2
			AND updated_at = $3
			AND platform = $4
			AND type = $5
			AND proxy_id IS NOT DISTINCT FROM $6
			AND deleted_at IS NULL
	`, string(payload), account.ID, account.UpdatedAt, account.Platform, account.Type, nullableProbeProxyID(account.ProxyID))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	return nil
}

type upstreamBillingProbeProxyIdentity struct {
	protocol string
	host     string
	port     int
	username string
	password string
	status   string
}

func lockAndMatchUpstreamBillingProbeProxy(ctx context.Context, client *dbent.Client, account *service.Account) (bool, error) {
	if account.ProxyID == nil {
		return account.Proxy == nil, nil
	}
	rows, err := client.QueryContext(ctx, `
		SELECT protocol, host, port, COALESCE(username, ''), COALESCE(password, ''), status
		FROM proxies
		WHERE id = $1 AND deleted_at IS NULL
		FOR SHARE
	`, *account.ProxyID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
		return false, nil
	}
	var current upstreamBillingProbeProxyIdentity
	if err := rows.Scan(&current.protocol, &current.host, &current.port, &current.username, &current.password, &current.status); err != nil {
		return false, err
	}
	expected := upstreamBillingProbeProxyIdentity{
		protocol: account.Proxy.Protocol,
		host:     account.Proxy.Host,
		port:     account.Proxy.Port,
		username: account.Proxy.Username,
		password: account.Proxy.Password,
		status:   account.Proxy.Status,
	}
	return current == expected, rows.Err()
}

func nullableProbeProxyID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
