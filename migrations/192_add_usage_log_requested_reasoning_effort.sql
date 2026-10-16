-- Preserve the client-explicit reasoning effort separately from the final
-- effort sent upstream. Historical rows remain NULL because they cannot be
-- reconstructed reliably after routing and protocol conversion.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS requested_reasoning_effort VARCHAR(20);
