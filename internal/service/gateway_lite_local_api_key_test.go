package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gatewayLiteLocalKeyRepoStub struct {
	APIKeyRepository
	keys    map[int64]*APIKey
	nextID  int64
	updates []APIKeyUpdateFields
}

func newGatewayLiteLocalKeyRepoStub() *gatewayLiteLocalKeyRepoStub {
	return &gatewayLiteLocalKeyRepoStub{keys: make(map[int64]*APIKey), nextID: 1}
}

func cloneGatewayLiteLocalKey(key *APIKey) *APIKey {
	if key == nil {
		return nil
	}
	clone := *key
	clone.IPWhitelist = append([]string(nil), key.IPWhitelist...)
	clone.IPBlacklist = append([]string(nil), key.IPBlacklist...)
	return &clone
}

func (s *gatewayLiteLocalKeyRepoStub) Create(_ context.Context, key *APIKey) error {
	if key.ID == 0 {
		key.ID = s.nextID
		s.nextID++
	}
	now := time.Now()
	key.CreatedAt = now
	key.UpdatedAt = now
	s.keys[key.ID] = cloneGatewayLiteLocalKey(key)
	return nil
}

func (s *gatewayLiteLocalKeyRepoStub) GetByID(_ context.Context, id int64) (*APIKey, error) {
	key := s.keys[id]
	if key == nil {
		return nil, ErrAPIKeyNotFound
	}
	return cloneGatewayLiteLocalKey(key), nil
}

func (s *gatewayLiteLocalKeyRepoStub) GetKeyAndOwnerID(_ context.Context, id int64) (string, int64, error) {
	key := s.keys[id]
	if key == nil {
		return "", 0, ErrAPIKeyNotFound
	}
	return key.Key, key.UserID, nil
}

func (s *gatewayLiteLocalKeyRepoStub) Update(_ context.Context, key *APIKey, fields APIKeyUpdateFields) error {
	s.updates = append(s.updates, fields)
	key.UpdatedAt = time.Now()
	s.keys[key.ID] = cloneGatewayLiteLocalKey(key)
	return nil
}

func (s *gatewayLiteLocalKeyRepoStub) DeleteWithAudit(_ context.Context, id int64) error {
	delete(s.keys, id)
	return nil
}

func (s *gatewayLiteLocalKeyRepoStub) ExistsByKey(_ context.Context, raw string) (bool, error) {
	for _, key := range s.keys {
		if key.Key == raw {
			return true, nil
		}
	}
	return false, nil
}

func (s *gatewayLiteLocalKeyRepoStub) ListByUserIDAndKeyPrefix(_ context.Context, userID int64, prefix string) ([]APIKey, error) {
	result := make([]APIKey, 0)
	for _, key := range s.keys {
		if key.UserID == userID && strings.HasPrefix(key.Key, prefix) {
			result = append(result, *cloneGatewayLiteLocalKey(key))
		}
	}
	return result, nil
}

type gatewayLiteLocalKeyUserRepoStub struct {
	UserRepository
	users map[int64]*User
}

func (s *gatewayLiteLocalKeyUserRepoStub) GetByID(_ context.Context, id int64) (*User, error) {
	user := s.users[id]
	if user == nil {
		return nil, ErrUserNotFound
	}
	clone := *user
	return &clone, nil
}

type gatewayLiteLocalKeyGroupRepoStub struct {
	GroupRepository
	groups map[int64]*Group
}

func (s *gatewayLiteLocalKeyGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group := s.groups[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	clone := *group
	return &clone, nil
}

func newGatewayLiteLocalKeyServiceForTest(admin *User, groups ...*Group) (*APIKeyService, *gatewayLiteLocalKeyRepoStub) {
	repo := newGatewayLiteLocalKeyRepoStub()
	users := map[int64]*User{}
	if admin != nil {
		users[admin.ID] = admin
	}
	groupMap := make(map[int64]*Group, len(groups))
	for _, group := range groups {
		groupMap[group.ID] = group
	}
	return NewAPIKeyService(
		repo,
		&gatewayLiteLocalKeyUserRepoStub{users: users},
		&gatewayLiteLocalKeyGroupRepoStub{groups: groupMap},
		nil,
		nil,
		nil,
		&config.Config{},
	), repo
}

func TestGatewayLiteLocalAPIKeyService_CreateUpdateListDelete(t *testing.T) {
	admin := &User{ID: 1, Role: RoleAdmin, Status: StatusActive}
	groupA := &Group{ID: 11, Name: "OpenAI A", Platform: PlatformOpenAI, Status: StatusActive}
	groupB := &Group{ID: 12, Name: "OpenAI B", Platform: PlatformOpenAI, Status: StatusActive}
	svc, repo := newGatewayLiteLocalKeyServiceForTest(admin, groupA, groupB)

	created, err := svc.CreateGatewayLiteLocalAPIKey(context.Background(), admin.ID, CreateGatewayLiteLocalAPIKeyRequest{
		Name:        "local automation",
		GroupID:     groupA.ID,
		IPWhitelist: []string{"127.0.0.1"},
	})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.Key, GatewayLiteLocalAPIKeyPrefix))
	require.Len(t, created.Key, len(GatewayLiteLocalAPIKeyPrefix)+64)
	require.Equal(t, groupA.ID, *created.GroupID)
	require.Equal(t, StatusAPIKeyActive, created.Status)

	name := "updated local automation"
	status := StatusAPIKeyDisabled
	updated, err := svc.UpdateGatewayLiteLocalAPIKey(context.Background(), created.ID, admin.ID, UpdateGatewayLiteLocalAPIKeyRequest{
		Name:    &name,
		GroupID: &groupB.ID,
		Status:  &status,
	})
	require.NoError(t, err)
	require.Equal(t, name, updated.Name)
	require.Equal(t, groupB.ID, *updated.GroupID)
	require.Equal(t, StatusAPIKeyDisabled, updated.Status)
	require.Equal(t, []APIKeyUpdateFields{{Name: true, Status: true, GroupID: true}}, repo.updates)

	listed, err := svc.ListGatewayLiteLocalAPIKeys(context.Background(), admin.ID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, created.ID, listed[0].ID)

	revealed, err := svc.RevealGatewayLiteLocalAPIKey(context.Background(), created.ID, admin.ID)
	require.NoError(t, err)
	require.Equal(t, created.Key, revealed)

	require.NoError(t, svc.DeleteGatewayLiteLocalAPIKey(context.Background(), created.ID, admin.ID))
	require.Empty(t, repo.keys)
}

func TestGatewayLiteLocalAPIKeyService_RevealFailsClosed(t *testing.T) {
	admin := &User{ID: 31, Role: RoleAdmin, Status: StatusActive}
	svc, repo := newGatewayLiteLocalKeyServiceForTest(admin)

	repo.keys[1] = &APIKey{ID: 1, UserID: admin.ID + 1, Key: GatewayLiteLocalAPIKeyPrefix + "other-owner"}
	_, err := svc.RevealGatewayLiteLocalAPIKey(context.Background(), 1, admin.ID)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)

	repo.keys[2] = &APIKey{ID: 2, UserID: admin.ID, Key: "sk-ordinary-key"}
	_, err = svc.RevealGatewayLiteLocalAPIKey(context.Background(), 2, admin.ID)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)

	_, err = svc.RevealGatewayLiteLocalAPIKey(context.Background(), 404, admin.ID)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)

	inactiveAdmin := &User{ID: 32, Role: RoleAdmin, Status: StatusDisabled}
	inactiveService, _ := newGatewayLiteLocalKeyServiceForTest(inactiveAdmin)
	_, err = inactiveService.RevealGatewayLiteLocalAPIKey(context.Background(), 1, inactiveAdmin.ID)
	require.ErrorIs(t, err, ErrInsufficientPerms)
}

func TestGatewayLiteLocalAPIKeyService_RejectsNonAdminAndUnavailableGroup(t *testing.T) {
	regularUser := &User{ID: 2, Role: RoleUser, Status: StatusActive}
	activeGroup := &Group{ID: 21, Name: "active", Platform: PlatformOpenAI, Status: StatusActive}
	svc, _ := newGatewayLiteLocalKeyServiceForTest(regularUser, activeGroup)

	_, err := svc.CreateGatewayLiteLocalAPIKey(context.Background(), regularUser.ID, CreateGatewayLiteLocalAPIKeyRequest{
		Name:    "forbidden",
		GroupID: activeGroup.ID,
	})
	require.ErrorIs(t, err, ErrInsufficientPerms)

	admin := &User{ID: 3, Role: RoleAdmin, Status: StatusActive}
	disabledGroup := &Group{ID: 22, Name: "disabled", Platform: PlatformOpenAI, Status: StatusDisabled}
	svc, _ = newGatewayLiteLocalKeyServiceForTest(admin, disabledGroup)
	_, err = svc.CreateGatewayLiteLocalAPIKey(context.Background(), admin.ID, CreateGatewayLiteLocalAPIKeyRequest{
		Name:    "invalid group",
		GroupID: disabledGroup.ID,
	})
	require.ErrorIs(t, err, ErrGatewayLiteLocalKeyGroupUnavailable)
}
