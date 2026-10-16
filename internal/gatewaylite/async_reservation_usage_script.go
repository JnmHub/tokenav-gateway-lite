package gatewaylite

import "github.com/redis/go-redis/v9"

// 专属脚本与普通 correction/Final 分离，但使用同一请求记录、零钱键及
// Stream 标记。Lua 出错不会回滚：所有类型、身份、金额和状态检查在写区前完成。
var commitAsyncReservationUsageScript = redis.NewScript(`
local req_key, lease_key, stream_key, remainder_key = KEYS[1], KEYS[2], KEYS[3], KEYS[4]
local body, request_id, key_id = ARGV[1], ARGV[2], ARGV[3]
local user_id, organization_id, subject_type, subject_id = ARGV[4], ARGV[5], ARGV[6], ARGV[7]
local region, lease_id, mode, raw_text, scope = ARGV[8], ARGV[9], ARGV[10], ARGV[11], ARGV[12]
local scale, maximum = 1000000, 9007199254740991
local function fail(message)
  error("SETTLEMENT_CONFLICT async reservation " .. message, 0)
end
local function kind(key)
  local reply = redis.call("TYPE", key)
  if type(reply) == "table" then return reply["ok"] end
  return reply
end
local function uint_text(text)
  if not text or not string.match(text, "^%d+$") then return nil end
  local n = tonumber(text)
  if not n or n < 0 or n > maximum or n ~= math.floor(n) or string.format("%.0f", n) ~= text then return nil end
  return n
end
local function unsigned(text, label, upper)
  local n = uint_text(text)
  if not n or n > (upper or maximum) then fail(label .. " is not an exact unsigned integer") end
  return n
end
local function number_field(key, field, optional)
  local text = redis.call("HGET", key, field)
  if not text and optional then return 0 end
  return unsigned(text, field)
end
local function match_field(key, field, expected)
  if redis.call("HGET", key, field) ~= expected then fail(field .. " does not match original identity") end
end
local function flag(key, field)
  local value = redis.call("HGET", key, field)
  if value and value ~= "0" and value ~= "1" then fail(field .. " is not a boolean marker") end
  return value == "1"
end
local function decimal(n) return string.format("%.0f", n) end

for i = 1, 4 do
  for j = i + 1, 4 do
    if KEYS[i] == KEYS[j] then fail("keys overlap") end
  end
end
if kind(req_key) ~= "hash" then fail("original request is missing or has wrong type") end
if kind(lease_key) ~= "hash" then fail("original lease is missing or has wrong type") end
local stream_type, remainder_type = kind(stream_key), kind(remainder_key)
if stream_type ~= "none" and stream_type ~= "stream" then fail("stream has wrong type") end
if remainder_type ~= "none" and remainder_type ~= "string" then fail("remainder has wrong type") end

-- 身份按原十进制字符串比较，避免 Lua double 合并不同的 64 位主体编号。
match_field(req_key, "request_id", request_id)
match_field(req_key, "key_id", key_id)
match_field(req_key, "lease_key", lease_key)
for _, key in ipairs({req_key, lease_key}) do
  match_field(key, "user_id", user_id)
  match_field(key, "organization_id", organization_id)
  match_field(key, "billing_subject_type", subject_type)
  match_field(key, "billing_subject_id", subject_id)
  match_field(key, "region", region)
  match_field(key, "lease_id", lease_id)
end
if redis.call("HEXISTS", req_key, "funding_authorization_id") == 1 or
   redis.call("HEXISTS", req_key, "child_actual_cents") == 1 or
   redis.call("HEXISTS", req_key, "has_child_settlements") == 1 then
  fail("funded or child settlement cannot own this hold")
end
if flag(req_key, "usage_commit_enqueued") then fail("another finalizer owns the request") end
local status = redis.call("HGET", req_key, "status")
if status ~= "reserved" and status ~= "committed" then fail("request is not reserved or committed") end
local allocated = number_field(lease_key, "allocated_cents")
local reserved = number_field(lease_key, "reserved_cents")
local spent = number_field(lease_key, "spent_cents")
local hold = number_field(req_key, "estimated_cents")
local request_actual = number_field(req_key, "actual_cents")
local version = number_field(lease_key, "version")
local expires = number_field(lease_key, "expires_at")
local target_cents = number_field(lease_key, "target_cents", true)
local refill_at = number_field(lease_key, "refill_at_cents", true)
if reserved > allocated or spent > allocated - reserved then fail("lease counters exceed allocation") end
local raw = unsigned(raw_text, "raw micro cents", maximum - scale)
local ttl = unsigned(ARGV[13], "remainder ttl")
if ttl == 0 then fail("remainder ttl is zero") end
local remainder = 0
if remainder_type == "string" then remainder = unsigned(redis.call("GET", remainder_key), "remainder", scale - 1) end

local is_v2 = mode == "idempotent_v2"
if not is_v2 and mode ~= "legacy" then fail("unknown frozen mode") end
local raw_field, actual_field, scope_field, mode_field, queue_field
if is_v2 then
  raw_field, actual_field, scope_field, mode_field = "settlement_micro_cents", "settlement_actual_cents", "settlement_scope", "settlement_mode"
  queue_field = "settlement_usage_enqueued"
  if redis.call("HEXISTS", req_key, "async_reservation_micro_cents") == 1 or flag(req_key, "usage_report_enqueued") then
    fail("legacy evidence conflicts with v2")
  end
else
  raw_field, actual_field, scope_field, mode_field = "async_reservation_micro_cents", "async_reservation_actual_cents", "async_reservation_scope", "async_reservation_mode"
  queue_field = "usage_report_enqueued"
  for _, field in ipairs({"settlement_micro_cents", "settlement_actual_cents", "settlement_scope", "settlement_mode", "settlement_quota_applied", "settlement_usage_enqueued"}) do
    if redis.call("HEXISTS", req_key, field) == 1 then fail("v2 evidence conflicts with legacy") end
  end
end
local enqueued = flag(req_key, queue_field)
-- XDEL/修剪后仍保留 last-generated-id；只查最后一条消息会漏掉已耗尽的空队列。
-- 64 位 Stream ID 必须按字符串比较，不能先转 Lua double。
if not enqueued and stream_type == "stream" then
  local info = redis.call("XINFO", "STREAM", stream_key)
  local last_id
  for i = 1, #info, 2 do
    if info[i] == "last-generated-id" then last_id = info[i + 1] end
  end
  if not last_id or last_id == "18446744073709551615-18446744073709551615" then
    fail("stream has no available automatic id")
  end
end
local quota_applied = is_v2 and flag(req_key, "settlement_quota_applied")
local async_committed = flag(req_key, "async_reservation_committed")
local frozen_raw = redis.call("HGET", req_key, raw_field)
local frozen_actual = redis.call("HGET", req_key, actual_field)
local frozen_scope = redis.call("HGET", req_key, scope_field)
local frozen_mode = redis.call("HGET", req_key, mode_field)
local frozen = frozen_raw or frozen_actual or frozen_scope or frozen_mode
local actual, next_remainder
if frozen then
  -- 包括原始金额 0 的重放，也必须是完全相同的财务事实。
  if unsigned(frozen_raw, "frozen micro cents", maximum - scale) ~= raw or frozen_scope ~= scope or frozen_mode ~= mode then
    fail("same request has different raw cost, scope or mode")
  end
  actual = unsigned(frozen_actual, "frozen actual cents")
  if actual < math.floor(raw / scale) or actual > math.ceil(raw / scale) then fail("frozen amount has no valid raw evidence") end
else
  if enqueued or quota_applied or async_committed or status == "committed" or
     redis.call("HEXISTS", req_key, "corrected_actual_cents") == 1 then
    fail("historical state has no reliable raw amount")
  end
  local total = remainder + raw
  actual = math.floor(total / scale)
  next_remainder = total - actual * scale
end
local corrected_text = redis.call("HGET", req_key, "corrected_actual_cents")
if corrected_text and unsigned(corrected_text, "corrected actual cents") ~= actual then fail("corrected amount conflicts with frozen amount") end
if status == "reserved" then
  if async_committed or request_actual ~= 0 or reserved < hold then fail("reserved hold is not intact") end
  if quota_applied and (not corrected_text or hold ~= actual) then fail("applied v2 correction is incomplete") end
elseif not frozen or request_actual ~= actual or spent < actual then
  fail("committed request has inconsistent monetary evidence")
end
local next_reserved, next_spent = reserved, spent
if status == "reserved" then
  next_reserved = reserved - hold
  if actual > maximum - spent then fail("spent amount exceeds exact range") end
  next_spent = spent + actual
  if next_reserved > allocated or next_spent > allocated - next_reserved then fail("lease has insufficient final capacity") end
end

-- 先生成返回值；写区只执行已预检类型的固定写命令，不再作业务验证。
local result = {1, 0}
if refill_at > 0 and allocated - next_reserved - next_spent <= refill_at then
  result = {1, 1, lease_id, user_id, region, allocated, next_reserved, next_spent, version, expires, target_cents, refill_at, organization_id, subject_type, subject_id}
end
if not frozen then
  if is_v2 or raw > 0 then redis.call("SET", remainder_key, decimal(next_remainder), "EX", ttl) end
  redis.call("HSET", req_key, raw_field, decimal(raw), actual_field, decimal(actual), scope_field, scope, mode_field, mode)
end
if status == "reserved" then
  redis.call("HSET", lease_key, "reserved_cents", decimal(next_reserved), "spent_cents", decimal(next_spent))
  redis.call("HSET", req_key, "status", "committed", "actual_cents", decimal(actual), "corrected_actual_cents", decimal(actual))
end
redis.call("HSET", req_key, "async_reservation_committed", "1")
if is_v2 then redis.call("HSET", req_key, "settlement_quota_applied", "1") end
if not enqueued then
  redis.call("XADD", stream_key, "*", "event", body, "resolved_actual_cents", decimal(actual))
  redis.call("HSET", req_key, queue_field, "1")
end
return result
`)
