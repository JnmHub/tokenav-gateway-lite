ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS peak_rate_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS peak_start VARCHAR(5) NOT NULL DEFAULT '18:00',
    ADD COLUMN IF NOT EXISTS peak_end VARCHAR(5) NOT NULL DEFAULT '22:00',
    ADD COLUMN IF NOT EXISTS peak_rate_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1.0,
    ADD COLUMN IF NOT EXISTS peak_timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'groups_peak_rate_multiplier_valid'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_peak_rate_multiplier_valid
            CHECK (peak_rate_multiplier > 0 AND peak_rate_multiplier <= 100);
    END IF;
END $$;
