//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateProfileDeclaresOnlyRequestedColumns(t *testing.T) {
	username := "renamed"
	repo := &updateFieldsUserRepoStub{user: &User{ID: 7, Username: "before", Balance: 9, Status: StatusActive}}
	svc := NewUserService(repo, nil, nil, nil)

	_, err := svc.UpdateProfile(context.Background(), 7, UpdateProfileRequest{Username: &username})
	require.NoError(t, err)
	require.Equal(t, []UserUpdateFields{{Username: true}}, repo.fields)
}

type updateFieldsUserRepoStub struct {
	UserRepository
	user   *User
	fields []UserUpdateFields
}

func (s *updateFieldsUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	clone := *s.user
	return &clone, nil
}

func (s *updateFieldsUserRepoStub) ExistsByEmail(context.Context, string) (bool, error) {
	return false, nil
}

func (s *updateFieldsUserRepoStub) Update(_ context.Context, user *User, fields UserUpdateFields) error {
	s.fields = append(s.fields, fields)
	s.user = user
	return nil
}

type updateFieldsAPIKeyRepoStub struct {
	APIKeyRepository
	key    *APIKey
	fields []APIKeyUpdateFields
}

func (s *updateFieldsAPIKeyRepoStub) GetByID(context.Context, int64) (*APIKey, error) {
	clone := *s.key
	return &clone, nil
}

func (s *updateFieldsAPIKeyRepoStub) Update(_ context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	s.fields = append(s.fields, fields)
	clone := *key
	s.key = &clone
	return nil
}

func TestAPIKeyUpdateDoesNotDeclareUsageColumnsForNameEdit(t *testing.T) {
	name := "renamed"
	repo := &updateFieldsAPIKeyRepoStub{key: &APIKey{
		ID: 1, UserID: 7, Key: "sk-test", Name: "before", Status: StatusActive,
		Quota: 100, QuotaUsed: 30, Usage5h: 12,
	}}
	svc := &APIKeyService{apiKeyRepo: repo}

	_, err := svc.Update(context.Background(), 1, 7, UpdateAPIKeyRequest{Name: &name})
	require.NoError(t, err)
	require.Equal(t, []APIKeyUpdateFields{{Name: true}}, repo.fields)
}

func TestAPIKeyUnlimitedQuotaReactivatesExhaustedKey(t *testing.T) {
	unlimited := 0.0
	repo := &updateFieldsAPIKeyRepoStub{key: &APIKey{
		ID: 1, UserID: 7, Key: "sk-test", Status: StatusAPIKeyQuotaExhausted,
		Quota: 100, QuotaUsed: 100,
	}}
	svc := &APIKeyService{apiKeyRepo: repo}

	updated, err := svc.Update(context.Background(), 1, 7, UpdateAPIKeyRequest{Quota: &unlimited})
	require.NoError(t, err)
	require.Equal(t, StatusActive, updated.Status)
	require.Equal(t, []APIKeyUpdateFields{{Status: true, Quota: true}}, repo.fields)
}

func TestGatewayLiteLocalKeyDeclaresOnlyRequestedColumns(t *testing.T) {
	groupID := int64(11)
	name := "renamed local key"
	status := StatusAPIKeyDisabled
	whitelist := []string{"127.0.0.1"}
	keyRepo := &updateFieldsAPIKeyRepoStub{key: &APIKey{
		ID: 1, UserID: 7, Key: GatewayLiteLocalAPIKeyPrefix + "test", Name: "before",
		Status: StatusAPIKeyActive, GroupID: &groupID,
	}}
	userRepo := &updateFieldsUserRepoStub{user: &User{ID: 7, Role: RoleAdmin, Status: StatusActive}}
	svc := &APIKeyService{apiKeyRepo: keyRepo, userRepo: userRepo}

	updated, err := svc.UpdateGatewayLiteLocalAPIKey(context.Background(), 1, 7, UpdateGatewayLiteLocalAPIKeyRequest{
		Name: &name, Status: &status, IPWhitelist: &whitelist,
	})
	require.NoError(t, err)
	require.Equal(t, name, updated.Name)
	require.Equal(t, []APIKeyUpdateFields{{Name: true, Status: true, IPRules: true}}, keyRepo.fields)
}
