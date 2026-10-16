//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type duplicateChannelMonitorRepoStub struct {
	ChannelMonitorRepository
	mu          sync.Mutex
	source      *ChannelMonitor
	byOperation map[string]*ChannelMonitor
	createCalls int
	nextID      int64
}

func cloneDuplicateMonitorTestValue(source *ChannelMonitor) *ChannelMonitor {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.ExtraModels = append([]string(nil), source.ExtraModels...)
	cloned.ExtraHeaders = cloneChannelMonitorHeaders(source.ExtraHeaders)
	if source.BodyOverride != nil {
		cloned.BodyOverride, _ = cloneChannelMonitorJSONValue(source.BodyOverride).(map[string]any)
	}
	if source.TemplateID != nil {
		id := *source.TemplateID
		cloned.TemplateID = &id
	}
	return &cloned
}

func (r *duplicateChannelMonitorRepoStub) GetByID(_ context.Context, id int64) (*ChannelMonitor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.source == nil || r.source.ID != id {
		return nil, ErrChannelMonitorNotFound
	}
	return cloneDuplicateMonitorTestValue(r.source), nil
}

func (r *duplicateChannelMonitorRepoStub) Create(_ context.Context, monitor *ChannelMonitor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byOperation[monitor.DuplicateOperationID]; exists {
		return errors.New("duplicate key value violates unique constraint")
	}
	r.createCalls++
	r.nextID++
	monitor.ID = r.nextID
	monitor.CreatedAt = time.Unix(r.nextID, 0).UTC()
	monitor.UpdatedAt = monitor.CreatedAt
	r.byOperation[monitor.DuplicateOperationID] = cloneDuplicateMonitorTestValue(monitor)
	return nil
}

func (r *duplicateChannelMonitorRepoStub) FindByDuplicateOperationID(_ context.Context, operationID string) (*ChannelMonitor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneDuplicateMonitorTestValue(r.byOperation[operationID]), nil
}

func TestDuplicateChannelMonitorCopiesEncryptedConfigurationWithoutHistory(t *testing.T) {
	checkedAt := time.Now().UTC()
	templateID := int64(19)
	repo := &duplicateChannelMonitorRepoStub{
		source: &ChannelMonitor{
			ID: 42, Name: "Primary Monitor", Provider: MonitorProviderOpenAI,
			APIMode: MonitorAPIModeResponses, Endpoint: "https://example.com/v1",
			APIKey: "encrypted:key-material", PrimaryModel: "gpt-5.6-sol",
			ExtraModels: []string{"gpt-5.5", "gpt-5.4"}, GroupName: "production",
			Enabled: true, IntervalSeconds: 90, JitterSeconds: 12,
			LastCheckedAt: &checkedAt, CreatedBy: 3, TemplateID: &templateID,
			ExtraHeaders:     map[string]string{"X-Region": "us-east"},
			BodyOverrideMode: MonitorBodyOverrideModeMerge,
			BodyOverride:     map[string]any{"metadata": map[string]any{"tier": "gold"}},
		},
		byOperation: make(map[string]*ChannelMonitor),
		nextID:      100,
	}
	svc := NewChannelMonitorService(repo, nil)

	duplicate, err := svc.Duplicate(context.Background(), 42, 77, "admin:77", "monitor-copy-42")
	require.NoError(t, err)
	require.Equal(t, int64(101), duplicate.ID)
	require.Equal(t, "Primary Monitor (Copy)", duplicate.Name)
	require.False(t, duplicate.Enabled)
	require.Nil(t, duplicate.LastCheckedAt)
	require.Equal(t, int64(77), duplicate.CreatedBy)
	require.Empty(t, duplicate.APIKey, "响应不能携带明文或密文 API Key")
	require.Equal(t, repo.source.Provider, duplicate.Provider)
	require.Equal(t, repo.source.APIMode, duplicate.APIMode)
	require.Equal(t, repo.source.PrimaryModel, duplicate.PrimaryModel)
	require.Equal(t, repo.source.ExtraModels, duplicate.ExtraModels)
	require.Equal(t, repo.source.GroupName, duplicate.GroupName)
	require.Equal(t, repo.source.ExtraHeaders, duplicate.ExtraHeaders)
	require.Equal(t, repo.source.BodyOverride, duplicate.BodyOverride)

	operationID := duplicateChannelMonitorOperationID(42, "admin:77", "monitor-copy-42")
	stored := repo.byOperation[operationID]
	require.NotNil(t, stored)
	require.Equal(t, "encrypted:key-material", stored.APIKey, "数据库复制必须复用密文，不经过浏览器")
	require.Nil(t, stored.LastCheckedAt)
	require.False(t, stored.Enabled)
	stored.ExtraHeaders["X-Region"] = "changed"
	require.Equal(t, "us-east", repo.source.ExtraHeaders["X-Region"], "Header 快照不能共享可变 map")
	stored.BodyOverride["metadata"].(map[string]any)["tier"] = "changed"
	require.Equal(t, "gold", repo.source.BodyOverride["metadata"].(map[string]any)["tier"], "Body 快照必须深复制")
}

func TestDuplicateChannelMonitorConcurrentSameOperationCreatesOneCopy(t *testing.T) {
	repo := &duplicateChannelMonitorRepoStub{
		source: &ChannelMonitor{
			ID: 9, Name: "Concurrent", Provider: MonitorProviderAnthropic,
			APIMode: MonitorAPIModeChatCompletions, Endpoint: "https://example.com",
			APIKey: "encrypted:shared", PrimaryModel: "claude-sonnet-4-5",
			IntervalSeconds: 60, BodyOverrideMode: MonitorBodyOverrideModeOff,
		},
		byOperation: make(map[string]*ChannelMonitor),
		nextID:      200,
	}
	svc := NewChannelMonitorService(repo, nil)

	const workers = 16
	results := make(chan *ChannelMonitor, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			monitor, err := svc.Duplicate(context.Background(), 9, 88, "admin:88", "same-operation")
			results <- monitor
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	var id int64
	for result := range results {
		require.NotNil(t, result)
		if id == 0 {
			id = result.ID
		}
		require.Equal(t, id, result.ID)
		require.False(t, result.Enabled)
		require.Empty(t, result.APIKey)
	}
	require.Equal(t, 1, repo.createCalls)
	require.Len(t, repo.byOperation, 1)
}

func TestDuplicateChannelMonitorNameUsesUnicodeRuneLimit(t *testing.T) {
	name := duplicateChannelMonitorName(string(make([]rune, 0)))
	require.Equal(t, " (Copy)", name)

	longName := "监控" + strings.Repeat("界", 120)
	duplicate := duplicateChannelMonitorName(longName)
	require.LessOrEqual(t, len([]rune(duplicate)), maxChannelMonitorNameRunes)
	require.True(t, strings.HasSuffix(duplicate, " (Copy)"))
}
