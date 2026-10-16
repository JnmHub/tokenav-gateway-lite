package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ReplaceGroupPricing 原子替换单个分组的自定义定价。
// 历史共享渠道不会被原地修改：当前分组会先解绑再创建独享渠道，其他分组继续使用旧渠道。
func (r *channelRepository) ReplaceGroupPricing(ctx context.Context, groupID int64, channel *service.Channel) (int64, error) {
	var resultID int64
	err := r.runInTx(ctx, func(tx *sql.Tx) error {
		currentID, shared, err := lockGroupPricingChannel(ctx, tx, groupID)
		if err != nil {
			return err
		}

		if channel == nil {
			if currentID == 0 {
				return nil
			}
			if shared {
				_, err = tx.ExecContext(ctx, `DELETE FROM channel_groups WHERE channel_id = $1 AND group_id = $2`, currentID, groupID)
				return err
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, currentID)
			return err
		}

		if currentID > 0 && !shared {
			channel.ID = currentID
			channel.Name = groupPricingChannelNameForRepository(groupID)
			if err := updateGroupPricingChannelTx(ctx, tx, channel); err != nil {
				return err
			}
			resultID = currentID
			return nil
		}

		if currentID > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM channel_groups WHERE channel_id = $1 AND group_id = $2`, currentID, groupID); err != nil {
				return fmt.Errorf("detach shared group pricing: %w", err)
			}
		}
		newID, err := createGroupPricingChannelTx(ctx, tx, groupID, channel)
		if err != nil {
			return err
		}
		resultID = newID
		return nil
	})
	return resultID, err
}

func lockGroupPricingChannel(ctx context.Context, tx *sql.Tx, groupID int64) (int64, bool, error) {
	var lockedGroupID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&lockedGroupID); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, service.ErrGroupNotFound
		}
		return 0, false, fmt.Errorf("lock group for pricing update: %w", err)
	}
	var channelID int64
	err := tx.QueryRowContext(ctx,
		`SELECT channel_id FROM channel_groups WHERE group_id = $1 FOR UPDATE`, groupID,
	).Scan(&channelID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("lock group pricing association: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM channel_groups WHERE channel_id = $1`, channelID,
	).Scan(&count); err != nil {
		return 0, false, fmt.Errorf("count group pricing associations: %w", err)
	}
	return channelID, count > 1, nil
}

func createGroupPricingChannelTx(ctx context.Context, tx *sql.Tx, groupID int64, channel *service.Channel) (int64, error) {
	modelMappingJSON, err := marshalModelMapping(channel.ModelMapping)
	if err != nil {
		return 0, err
	}
	featuresConfigJSON, err := marshalFeaturesConfig(channel.FeaturesConfig)
	if err != nil {
		return 0, err
	}
	var channelID int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO channels (name, description, status, model_mapping, billing_model_source, restrict_models, features, features_config, apply_pricing_to_account_stats)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		channel.Name, channel.Description, channel.Status, modelMappingJSON, channel.BillingModelSource,
		channel.RestrictModels, channel.Features, featuresConfigJSON, channel.ApplyPricingToAccountStats,
	).Scan(&channelID)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, service.ErrChannelExists
		}
		return 0, fmt.Errorf("insert group pricing channel: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO channel_groups (channel_id, group_id) VALUES ($1, $2)`, channelID, groupID,
	); err != nil {
		return 0, fmt.Errorf("insert group pricing association: %w", err)
	}
	if err := replaceModelPricingTx(ctx, tx, channelID, channel.ModelPricing); err != nil {
		return 0, err
	}
	if err := replaceAccountStatsPricingRulesTx(ctx, tx, channelID, channel.AccountStatsPricingRules); err != nil {
		return 0, err
	}
	return channelID, nil
}

func updateGroupPricingChannelTx(ctx context.Context, tx *sql.Tx, channel *service.Channel) error {
	modelMappingJSON, err := marshalModelMapping(channel.ModelMapping)
	if err != nil {
		return err
	}
	featuresConfigJSON, err := marshalFeaturesConfig(channel.FeaturesConfig)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE channels SET name = $1, description = $2, status = $3, model_mapping = $4,
		 billing_model_source = $5, restrict_models = $6, features = $7, features_config = $8,
		 apply_pricing_to_account_stats = $9, updated_at = NOW() WHERE id = $10`,
		channel.Name, channel.Description, channel.Status, modelMappingJSON, channel.BillingModelSource,
		channel.RestrictModels, channel.Features, featuresConfigJSON, channel.ApplyPricingToAccountStats, channel.ID,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return service.ErrChannelExists
		}
		return fmt.Errorf("update group pricing channel: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return service.ErrChannelNotFound
	}
	if err := replaceModelPricingTx(ctx, tx, channel.ID, channel.ModelPricing); err != nil {
		return err
	}
	return replaceAccountStatsPricingRulesTx(ctx, tx, channel.ID, channel.AccountStatsPricingRules)
}

func groupPricingChannelNameForRepository(groupID int64) string {
	return fmt.Sprintf("group-pricing-%d", groupID)
}
