package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type grokCustomVoiceStore struct {
	db *sql.DB
}

func NewGrokCustomVoiceStore(db *sql.DB) service.GrokCustomVoiceStore {
	return &grokCustomVoiceStore{db: db}
}

func (s *grokCustomVoiceStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("grok custom voice database is unavailable")
	}
	return s.db.PingContext(ctx)
}

func (s *grokCustomVoiceStore) Save(ctx context.Context, binding *service.GrokCustomVoiceBinding) error {
	if s == nil || s.db == nil {
		return errors.New("grok custom voice database is unavailable")
	}
	if binding == nil || strings.TrimSpace(binding.VoiceID) == "" || binding.UserID <= 0 || binding.AccountID <= 0 ||
		(binding.APIKeyID <= 0 && strings.TrimSpace(binding.GatewayKeyID) == "") {
		return errors.New("grok custom voice binding is incomplete")
	}
	voiceID := strings.TrimSpace(binding.VoiceID)
	gatewayKeyID := strings.TrimSpace(binding.GatewayKeyID)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO grok_custom_voice_bindings (
			voice_id, user_id, api_key_id, gateway_key_id, group_id, account_id, created_at, updated_at
		) VALUES ($1, $2, NULLIF($3, 0), NULLIF($4, ''), $5, $6, NOW(), NOW())
		ON CONFLICT (voice_id) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			api_key_id = EXCLUDED.api_key_id,
			gateway_key_id = EXCLUDED.gateway_key_id,
			group_id = EXCLUDED.group_id,
			account_id = EXCLUDED.account_id,
			updated_at = NOW()
	`, voiceID, binding.UserID, binding.APIKeyID, gatewayKeyID, binding.GroupID, binding.AccountID)
	if err != nil {
		return fmt.Errorf("persist grok custom voice binding: %w", err)
	}
	return nil
}

func (s *grokCustomVoiceStore) Get(ctx context.Context, voiceID string) (*service.GrokCustomVoiceBinding, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("grok custom voice database is unavailable")
	}
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		return nil, service.ErrGrokCustomVoiceNotFound
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT voice_id, user_id, COALESCE(api_key_id, 0), COALESCE(gateway_key_id, ''), group_id,
		       account_id, created_at, updated_at
		FROM grok_custom_voice_bindings
		WHERE voice_id = $1
	`, voiceID)
	binding := &service.GrokCustomVoiceBinding{}
	if err := row.Scan(&binding.VoiceID, &binding.UserID, &binding.APIKeyID, &binding.GatewayKeyID,
		&binding.GroupID, &binding.AccountID, &binding.CreatedAt, &binding.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrGrokCustomVoiceNotFound
		}
		return nil, err
	}
	return binding, nil
}

func (s *grokCustomVoiceStore) ListForOwner(ctx context.Context, userID, apiKeyID int64, gatewayKeyID string) ([]*service.GrokCustomVoiceBinding, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("grok custom voice database is unavailable")
	}
	gatewayKeyID = strings.TrimSpace(gatewayKeyID)
	rows, err := s.db.QueryContext(ctx, `
		SELECT voice_id, user_id, COALESCE(api_key_id, 0), COALESCE(gateway_key_id, ''), group_id,
		       account_id, created_at, updated_at
		FROM grok_custom_voice_bindings
		WHERE user_id = $1
		  AND (($3 <> '' AND gateway_key_id = $3) OR ($3 = '' AND api_key_id = NULLIF($2, 0)))
		ORDER BY created_at, voice_id
	`, userID, apiKeyID, gatewayKeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := make([]*service.GrokCustomVoiceBinding, 0)
	for rows.Next() {
		binding := &service.GrokCustomVoiceBinding{}
		if err := rows.Scan(&binding.VoiceID, &binding.UserID, &binding.APIKeyID, &binding.GatewayKeyID,
			&binding.GroupID, &binding.AccountID, &binding.CreatedAt, &binding.UpdatedAt); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *grokCustomVoiceStore) Delete(ctx context.Context, voiceID string) error {
	if s == nil || s.db == nil {
		return errors.New("grok custom voice database is unavailable")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM grok_custom_voice_bindings WHERE voice_id = $1`, strings.TrimSpace(voiceID))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return service.ErrGrokCustomVoiceNotFound
	}
	return nil
}

var _ service.GrokCustomVoiceStore = (*grokCustomVoiceStore)(nil)
