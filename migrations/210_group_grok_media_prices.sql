ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS video_model_prices JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS audio_realtime_price_per_min DECIMAL(20,8),
    ADD COLUMN IF NOT EXISTS audio_tts_price_per_million_chars DECIMAL(20,8),
    ADD COLUMN IF NOT EXISTS audio_stt_price_per_hour DECIMAL(20,8);

COMMENT ON COLUMN groups.video_model_prices IS
    'Grok 视频模型族到 480p/720p/1080p 每秒价格矩阵';
COMMENT ON COLUMN groups.audio_realtime_price_per_min IS
    'Grok Realtime 每分钟价格；NULL 使用默认价格，0 表示免费';
COMMENT ON COLUMN groups.audio_tts_price_per_million_chars IS
    'Grok TTS 每百万字符价格；NULL 使用默认价格，0 表示免费';
COMMENT ON COLUMN groups.audio_stt_price_per_hour IS
    'Grok STT 每小时价格；NULL 使用默认价格，0 表示免费';
