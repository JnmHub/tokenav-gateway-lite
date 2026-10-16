ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS allow_openai_long_context_billing BOOLEAN NOT NULL DEFAULT FALSE;
