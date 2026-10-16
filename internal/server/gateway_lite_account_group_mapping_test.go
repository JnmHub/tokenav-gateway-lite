package server

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteAccountPushSnapshotPreservesGroupModelMappings(t *testing.T) {
	account := &service.Account{
		ID:       41,
		Platform: service.PlatformOpenAI,
		GroupIDs: []int64{7, 8},
		AccountGroups: []service.AccountGroup{
			{AccountID: 41, GroupID: 7, Priority: 1, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-luna"}},
			{AccountID: 41, GroupID: 8, Priority: 9, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"}},
		},
	}

	snapshot := gatewayLiteAccountPushSnapshot(account)
	require.Equal(t, map[int64]int{7: 1, 8: 9}, snapshot.GroupPriorities)
	require.Equal(t, map[int64]map[string]string{
		7: {"gpt-5.6-sol": "gpt-5.6-luna"},
		8: {"gpt-5.6-sol": "gpt-5.6-terra"},
	}, snapshot.GroupModelMappings)

	snapshot.GroupModelMappings[7]["gpt-5.6-sol"] = "changed"
	require.Equal(t, "gpt-5.6-luna", account.AccountGroups[0].ModelMapping["gpt-5.6-sol"])
}

func TestGatewayLiteConfigPushFilterAccountGroupsDropsUnpublishedMappings(t *testing.T) {
	accounts := []gatewaylite.GatewayAccountSnapshot{{
		ID:                 41,
		GroupIDs:           []int64{7, 8},
		GroupPriorities:    map[int64]int{7: 1, 8: 9},
		GroupModelMappings: map[int64]map[string]string{7: {"gpt-5.6-sol": "gpt-5.6-luna"}, 8: {"gpt-5.6-sol": "gpt-5.6-terra"}},
	}}
	groups := []gatewaylite.GatewayGroupSnapshot{{ID: 7}}

	filtered := gatewayLiteConfigPushFilterAccountGroups(accounts, groups)
	require.Equal(t, []int64{7}, filtered[0].GroupIDs)
	require.Equal(t, map[int64]int{7: 1}, filtered[0].GroupPriorities)
	require.Equal(t, map[int64]map[string]string{7: {"gpt-5.6-sol": "gpt-5.6-luna"}}, filtered[0].GroupModelMappings)
}

func TestGatewayLiteAccountSnapshotToServiceAccountRestoresGroupModelMappings(t *testing.T) {
	snapshot := gatewaylite.GatewayAccountSnapshot{
		ID:                 41,
		Priority:           50,
		GroupIDs:           []int64{7},
		GroupPriorities:    map[int64]int{7: 1},
		GroupModelMappings: map[int64]map[string]string{7: {"gpt-5.6-sol": "gpt-5.6-luna"}},
	}

	account := gatewayLiteAccountSnapshotToServiceAccount(snapshot, nil)
	require.Len(t, account.AccountGroups, 1)
	require.Equal(t, 1, account.AccountGroups[0].Priority)
	require.Equal(t, "gpt-5.6-luna", account.AccountGroups[0].ModelMapping["gpt-5.6-sol"])

	snapshot.GroupModelMappings[7]["gpt-5.6-sol"] = "changed"
	require.Equal(t, "gpt-5.6-luna", account.AccountGroups[0].ModelMapping["gpt-5.6-sol"])
}

func TestGatewayLiteConfigPushEffectiveAccountsRestoresGroupPriorities(t *testing.T) {
	snapshot := gatewaylite.GatewayAccountSnapshot{
		ID:                 41,
		Priority:           50,
		GroupIDs:           []int64{7, 8},
		GroupPriorities:    map[int64]int{7: 1, 8: 9},
		GroupModelMappings: map[int64]map[string]string{7: {"gpt-5.6-sol": "gpt-5.6-luna"}},
	}

	accounts := gatewayLiteConfigPushEffectiveAccounts([]gatewaylite.GatewayAccountSnapshot{snapshot})
	require.Len(t, accounts, 1)
	require.Equal(t, []service.AccountGroup{
		{AccountID: 41, GroupID: 7, Priority: 1, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-luna"}},
		{AccountID: 41, GroupID: 8, Priority: 9},
	}, accounts[0].AccountGroups)
}

func TestGatewayLiteAccountSnapshotToServiceAccountFallsBackToAccountPriority(t *testing.T) {
	snapshot := gatewaylite.GatewayAccountSnapshot{
		ID:       41,
		Priority: 12,
		GroupIDs: []int64{7},
	}

	account := gatewayLiteAccountSnapshotToServiceAccount(snapshot, nil)
	require.Len(t, account.AccountGroups, 1)
	require.Equal(t, 12, account.AccountGroups[0].Priority)
}
