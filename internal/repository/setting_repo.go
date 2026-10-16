package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

// CompareAndSwapMultiple 在同一事务内锁住修订号并写入全部设置。
// 首次保存先以 0 建立修订行；并发创建由唯一键串行化，失败方重新读取后会得到冲突，
// 不会出现两个管理员都以 revision=0 覆盖成功的情况。
func (r *settingRepository) CompareAndSwapMultiple(ctx context.Context, revisionKey, expectedRevision, nextRevision string, values map[string]string) (swapped bool, err error) {
	if r == nil || r.client == nil {
		return false, errors.New("setting repository is unavailable")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	now := time.Now()
	if err = tx.Setting.Create().
		SetKey(revisionKey).
		SetValue("0").
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		Ignore().
		Exec(ctx); err != nil {
		return false, err
	}
	current, queryErr := tx.Setting.Query().
		Where(setting.KeyEQ(revisionKey)).
		ForUpdate().
		Only(ctx)
	if queryErr != nil {
		return false, queryErr
	}
	if current.Value != expectedRevision {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return false, rollbackErr
		}
		return false, nil
	}

	updates := make(map[string]string, len(values)+1)
	for key, value := range values {
		updates[key] = value
	}
	updates[revisionKey] = nextRevision
	builders := make([]*ent.SettingCreate, 0, len(updates))
	for key, value := range updates {
		builders = append(builders, tx.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	if err = tx.Setting.CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}
