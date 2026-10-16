ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS search_price_per_1k DECIMAL(20,8);

COMMENT ON COLUMN groups.search_price_per_1k IS
    'Grok 搜索工具每千次调用价格；NULL 使用默认价 10，0 表示免费';
