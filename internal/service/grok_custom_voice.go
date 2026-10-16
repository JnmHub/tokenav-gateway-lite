package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrGrokCustomVoiceNotFound = errors.New("grok custom voice not found")

// GrokCustomVoiceBinding is the durable authorization and routing record for
// an upstream custom voice. Voice IDs are provider resources, so every later
// read/update/delete must use the same owner, API key and upstream account.
type GrokCustomVoiceBinding struct {
	VoiceID      string
	UserID       int64
	APIKeyID     int64
	GatewayKeyID string
	GroupID      *int64
	AccountID    int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (b *GrokCustomVoiceBinding) BelongsTo(userID, apiKeyID int64, gatewayKeyID string) bool {
	if b == nil || b.UserID != userID {
		return false
	}
	if b.GatewayKeyID != "" {
		return strings.TrimSpace(gatewayKeyID) == b.GatewayKeyID
	}
	return b.APIKeyID > 0 && b.APIKeyID == apiKeyID
}

type GrokCustomVoiceStore interface {
	Ready(ctx context.Context) error
	Save(ctx context.Context, binding *GrokCustomVoiceBinding) error
	Get(ctx context.Context, voiceID string) (*GrokCustomVoiceBinding, error)
	ListForOwner(ctx context.Context, userID, apiKeyID int64, gatewayKeyID string) ([]*GrokCustomVoiceBinding, error)
	Delete(ctx context.Context, voiceID string) error
}

func (s *OpenAIGatewayService) SetGrokCustomVoiceStore(store GrokCustomVoiceStore) {
	if s != nil {
		s.grokCustomVoiceStore = store
	}
}

func (s *OpenAIGatewayService) CheckGrokCustomVoiceStore(ctx context.Context) error {
	if s == nil || s.grokCustomVoiceStore == nil {
		return errors.New("grok custom voice store is unavailable")
	}
	return s.grokCustomVoiceStore.Ready(ctx)
}

func (s *OpenAIGatewayService) SaveGrokCustomVoiceBinding(ctx context.Context, binding *GrokCustomVoiceBinding) error {
	if s == nil || s.grokCustomVoiceStore == nil {
		return errors.New("grok custom voice store is unavailable")
	}
	return s.grokCustomVoiceStore.Save(ctx, binding)
}

func (s *OpenAIGatewayService) GetGrokCustomVoiceBinding(ctx context.Context, voiceID string) (*GrokCustomVoiceBinding, error) {
	if s == nil || s.grokCustomVoiceStore == nil {
		return nil, errors.New("grok custom voice store is unavailable")
	}
	return s.grokCustomVoiceStore.Get(ctx, voiceID)
}

func (s *OpenAIGatewayService) ListGrokCustomVoiceBindings(ctx context.Context, userID, apiKeyID int64, gatewayKeyID string) ([]*GrokCustomVoiceBinding, error) {
	if s == nil || s.grokCustomVoiceStore == nil {
		return nil, errors.New("grok custom voice store is unavailable")
	}
	return s.grokCustomVoiceStore.ListForOwner(ctx, userID, apiKeyID, gatewayKeyID)
}

func (s *OpenAIGatewayService) DeleteGrokCustomVoiceBinding(ctx context.Context, voiceID string) error {
	if s == nil || s.grokCustomVoiceStore == nil {
		return errors.New("grok custom voice store is unavailable")
	}
	return s.grokCustomVoiceStore.Delete(ctx, voiceID)
}

func (s *OpenAIGatewayService) GetGrokCustomVoiceAccount(ctx context.Context, binding *GrokCustomVoiceBinding) (*Account, error) {
	if s == nil || s.accountRepo == nil || binding == nil || binding.AccountID <= 0 {
		return nil, errors.New("grok custom voice account is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, binding.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil || account.Platform != PlatformGrok {
		return nil, errors.New("grok custom voice account is invalid")
	}
	return account, nil
}
