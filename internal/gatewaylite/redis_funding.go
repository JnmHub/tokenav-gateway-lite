package gatewaylite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const maxFundingExactInteger int64 = 1<<53 - 1

func (q *RedisQuota) fundingKey(id string) string { return q.Prefix() + ":funding:" + id }
func (q *RedisQuota) fundingItemKey(id string, itemID int64) string {
	return fmt.Sprintf("%s:funding:%s:item:%d", q.Prefix(), id, itemID)
}

func validateFundingAuthorization(a FundingAuthorization) error {
	if a.AuthorizationID == "" || a.RequestID == "" || a.KeyID == "" || a.GatewayCode == "" || a.Region == "" || a.RequestedModel == "" || a.Endpoint == "" || a.UserID <= 0 || a.BillingSubjectID <= 0 || a.EntryGatewayNodeID <= 0 || a.EntryGroupID <= 0 || a.Version <= 0 || a.Disposition != "authorized" || a.UpperBoundMicroCents <= 0 || len(a.Items) == 0 {
		return errors.New("invalid funding authorization")
	}
	// Lua/cjson uses doubles, so imprecise identities are rejected just like amounts
	// before a script can claim a request or mutate any funding source.
	for _, value := range []int64{a.UserID, a.OrganizationID, a.BillingSubjectID, a.EntryGatewayNodeID, a.EntryGroupID, a.Version, a.Revision, a.PolicyVersion, a.UpperBoundMicroCents, a.MaximumCents, a.SettlementRevision, a.ActualMicroCents} {
		if value < 0 || value > maxFundingExactInteger {
			return errors.New("funding number exceeds exact range")
		}
	}
	if a.BillingSubjectType == "user" {
		if a.OrganizationID != 0 || a.BillingSubjectID != a.UserID {
			return errors.New("funding user subject conflicts")
		}
	} else if a.BillingSubjectType != "organization" || a.OrganizationID <= 0 || a.BillingSubjectID != a.OrganizationID {
		return errors.New("funding organization subject conflicts")
	}
	total := int64(0)
	seen := map[int64]bool{}
	for index, item := range a.Items {
		if item.ItemID <= 0 || seen[item.ItemID] || item.Ordinal != index+1 || item.ReservedMicroCents <= 0 || item.SettledMicroCents > item.ReservedMicroCents || (item.Source != "balance" && item.Source != "time_card") || (item.Source == "time_card" && item.EntitlementID <= 0) || (item.Source == "balance" && (item.Unlimited || item.EntitlementID != 0)) {
			return errors.New("invalid funding item")
		}
		for _, value := range []int64{item.ItemID, item.EntitlementID, item.CardVersion, item.ReservedMicroCents, item.SettledMicroCents, item.MaximumCents} {
			if value < 0 || value > maxFundingExactInteger {
				return errors.New("funding item number exceeds exact range")
			}
		}
		seen[item.ItemID] = true
		if total > maxFundingExactInteger-item.ReservedMicroCents {
			return errors.New("funding item total overflows")
		}
		total += item.ReservedMicroCents
	}
	if total < a.UpperBoundMicroCents {
		return errors.New("funding items do not cover upper bound")
	}
	return nil
}

// ReserveFunding mirrors one main-site allocation. It does not copy a card's
// global balance to Redis. Every item and the request claim are checked before
// any write: Lua runtime errors do not roll back preceding Redis mutations.
func (q *RedisQuota) ReserveFunding(ctx context.Context, a FundingAuthorization) error {
	if q == nil || !q.Enabled() {
		return ErrRequestQuotaUnavailable
	}
	if err := validateFundingAuthorization(a); err != nil {
		return err
	}
	if a.Status != "active" || a.ExecutionDeadlineMillis <= time.Now().UnixMilli() {
		return ErrRequestQuotaUnavailable
	}
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	scope := fundingScopeDigest(a)
	keys := []string{q.requestKey(a.RequestID), q.fundingKey(a.AuthorizationID)}
	for _, item := range a.Items {
		keys = append(keys, q.fundingItemKey(a.AuthorizationID, item.ItemID))
	}
	_, err = reserveFundingScript.Run(ctx, q.client, keys, string(body), scope, a.Version, a.UpperBoundMicroCents, a.AuthorizationID, a.RequestID, len(a.Items)).Int()
	return err
}
func fundingScopeDigest(a FundingAuthorization) string {
	text := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s\x00%d\x00%s\x00%s\x00%d\x00%d\x00%s\x00%s", a.RequestID, a.KeyID, a.UserID, a.OrganizationID, a.BillingSubjectType, a.BillingSubjectID, a.GatewayCode, a.Region, a.EntryGatewayNodeID, a.EntryGroupID, a.RequestedModel, a.Endpoint)
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

var reserveFundingScript = redis.NewScript(`
local function typ(k) local t=redis.call("TYPE",k);if type(t)=="table" then return t.ok end;return t end
local function integer(v) return string.format("%.0f",v or 0) end
for _,k in ipairs(KEYS) do local t=typ(k);if t~="none" and t~="hash" then return redis.error_reply("WRONGTYPE funding key") end end
local a=cjson.decode(ARGV[1]);local scope=ARGV[2];local version=tonumber(ARGV[3])
local req=KEYS[1];local auth=KEYS[2]
if redis.call("EXISTS",req)==1 then
 if redis.call("HGET",req,"funding_authorization_id")~=ARGV[5] or redis.call("HGET",req,"funding_scope")~=scope then return redis.error_reply("FUNDING_CONFLICT request already claimed") end
end
if redis.call("EXISTS",auth)==1 and redis.call("HGET",auth,"scope")~=scope then return redis.error_reply("FUNDING_CONFLICT scope") end
if redis.call("HGET",req,"funding_final")=="1" or redis.call("HGET",auth,"funding_final")=="1" then return redis.error_reply("FUNDING_FINAL request cannot execute again") end
-- A partial settlement is not permission to resend the same upstream operation.
if tonumber(redis.call("HGET",req,"funding_settlement_revision") or redis.call("HGET",auth,"funding_settlement_revision") or "0")>0 then return redis.error_reply("FUNDING_CONFLICT request already consumed") end
local old=tonumber(redis.call("HGET",auth,"version") or redis.call("HGET",req,"funding_version") or "0")
if old>version then return redis.error_reply("FUNDING_STALE authorization snapshot") end
if old==version and redis.call("EXISTS",auth)==1 and redis.call("HGET",auth,"snapshot")~=ARGV[1] then return redis.error_reply("FUNDING_CONFLICT same version changed") end
for i,item in ipairs(a.items) do
 local k=KEYS[i+2]
 if redis.call("EXISTS",k)==1 then
  if redis.call("HGET",k,"item_id")~=integer(item.item_id) or redis.call("HGET",k,"source")~=item.source or redis.call("HGET",k,"entitlement_id")~=integer(item.entitlement_id) or redis.call("HGET",k,"ordinal")~=integer(item.ordinal) or redis.call("HGET",k,"funding_scope")~=scope then return redis.error_reply("FUNDING_CONFLICT item") end
  if redis.call("HGET",k,"funding_final")=="1" then return redis.error_reply("FUNDING_FINAL item cannot execute again") end
 end
end
-- No early success on equal versions: eviction of any one hash must rebuild the
-- entire mirror, after every identity and type check succeeds.
redis.call("HSET",auth,"snapshot",ARGV[1],"scope",scope,"version",ARGV[3],"upper_bound_micro_cents",ARGV[4])
redis.call("HSET",req,"funding_authorization_id",ARGV[5],"funding_scope",scope,"funding_version",ARGV[3],"request_id",ARGV[6],"status","funding_reserved")
for i,item in ipairs(a.items) do redis.call("HSET",KEYS[i+2],"item_id",integer(item.item_id),"ordinal",integer(item.ordinal),"entitlement_id",integer(item.entitlement_id),"funding_scope",scope,"source",item.source,"reserved_micro_cents",integer(item.reserved_micro_cents),"settled_micro_cents",integer(item.settled_micro_cents)) end
return 1
`)

func (r *RedisUsageReporter) reportFundedUsage(ctx context.Context, event UsageEvent) (int64, error) {
	if !r.Enabled() {
		return 0, ErrRequestQuotaUnavailable
	}
	if event.FundingSnapshot == nil || event.ActualMicroCents == nil || event.SettlementRevision <= 0 || event.SettlementRevision > maxFundingExactInteger || *event.ActualMicroCents < 0 || *event.ActualMicroCents > maxFundingExactInteger {
		return 0, errors.New("funded usage snapshot and revision are required")
	}
	a := event.FundingSnapshot.Clone()
	if err := validateFundingAuthorization(a); err != nil {
		return 0, err
	}
	if event.RequestID != a.RequestID || event.FundingAuthorizationID != a.AuthorizationID || event.FundingRevision != a.Version || event.UserID != a.UserID || event.KeyID != a.KeyID || event.OrganizationID != a.OrganizationID || event.BillingSubjectID != a.BillingSubjectID || event.BillingSubjectType != a.BillingSubjectType {
		return 0, errors.New("funded usage identity conflicts with authorization")
	}
	body, err := json.Marshal(queuedUsageEvent{Event: event, EnqueuedAt: time.Now().UnixMilli()})
	if err != nil {
		return 0, err
	}
	q := NewRedisQuota(r.client, r.prefix())
	keys := []string{q.requestKey(a.RequestID), q.fundingKey(a.AuthorizationID), r.streamKey()}
	for _, item := range a.Items {
		keys = append(keys, q.fundingItemKey(a.AuthorizationID, item.ItemID))
	}
	authBody, err := json.Marshal(a)
	if err != nil {
		return 0, err
	}
	signatureEvent := event
	// Delivery timing can change on retries; financial and model evidence cannot.
	signatureEvent.LatencyMillis = 0
	signatureEvent.FirstTokenMillis = 0
	signatureEvent.AuthLatencyMillis = 0
	signatureEvent.QuotaLatencyMillis = 0
	signatureEvent.RoutingLatencyMillis = 0
	signatureEvent.RoutingBreakdownMillis = nil
	signatureEvent.UpstreamLatencyMillis = 0
	signatureEvent.ResponseLatencyMillis = 0
	signatureEvent.ReportLatencyMillis = 0
	signatureEvent.StartedAtMillis = 0
	signatureEvent.EndedAtMillis = 0
	signatureBody, err := json.Marshal(signatureEvent)
	if err != nil {
		return 0, err
	}
	signatureHash := sha256.Sum256(signatureBody)
	signature := hex.EncodeToString(signatureHash[:])
	actual, err := queueFundingUsageScript.Run(ctx, r.client, keys, string(body), string(authBody), fundingScopeDigest(a), event.SettlementRevision, *event.ActualMicroCents, boolToRedisInt(event.Final), a.AuthorizationID, a.RequestID, a.Version, actualCostMicroCentsScale, signature).Int64()
	return actual, err
}

var queueFundingUsageScript = redis.NewScript(`
local function typ(k) local t=redis.call("TYPE",k);if type(t)=="table" then return t.ok end;return t end
local function integer(v) return string.format("%.0f",v or 0) end
for i,k in ipairs(KEYS) do local t=typ(k);local expected="hash";if i==3 then expected="stream" end;if t~="none" and t~=expected then return redis.error_reply("WRONGTYPE funding usage key") end end
local wrapper=cjson.decode(ARGV[1]);local a=cjson.decode(ARGV[2]);local scope=ARGV[3]
local revision=tonumber(ARGV[4]);local actual=tonumber(ARGV[5]);local final=ARGV[6]
local req=KEYS[1];local auth=KEYS[2]
if redis.call("EXISTS",req)==1 then
 if redis.call("HGET",req,"funding_authorization_id")~=ARGV[7] or redis.call("HGET",req,"funding_scope")~=scope then return redis.error_reply("FUNDING_CONFLICT usage scope") end
end
if redis.call("EXISTS",auth)==1 and redis.call("HGET",auth,"scope")~=scope then return redis.error_reply("FUNDING_CONFLICT authorization scope") end
local prev=math.max(tonumber(redis.call("HGET",req,"funding_settlement_revision") or "0"),tonumber(redis.call("HGET",auth,"funding_settlement_revision") or "0"))
if revision<prev then return tonumber(redis.call("HGET",req,"actual_cents") or redis.call("HGET",auth,"actual_cents") or "0") end
local duplicate=revision==prev and prev>0
if duplicate and (redis.call("HGET",req,"funding_settlement_signature") or redis.call("HGET",auth,"funding_settlement_signature"))~=ARGV[11] then return redis.error_reply("FUNDING_CONFLICT settlement revision") end
local wasFinal=redis.call("HGET",req,"funding_final")=="1" or redis.call("HGET",auth,"funding_final")=="1"
if wasFinal and final~="1" then return redis.error_reply("FUNDING_FINAL cannot reopen settlement") end
local oldVersion=math.max(tonumber(redis.call("HGET",auth,"version") or "0"),tonumber(redis.call("HGET",req,"funding_version") or "0"))
local mirrorVersion=math.max(oldVersion,tonumber(ARGV[9]))
local remaining=actual;local itemAmounts={};local itemCaps={}
local previousAmounts=cjson.decode(redis.call("HGET",req,"funding_settled_items") or redis.call("HGET",auth,"funding_settled_items") or "[]")
for i,item in ipairs(a.items) do
 local k=KEYS[i+3]
 if redis.call("EXISTS",k)==1 then
  if redis.call("HGET",k,"item_id")~=integer(item.item_id) or redis.call("HGET",k,"source")~=item.source or redis.call("HGET",k,"entitlement_id")~=integer(item.entitlement_id) or redis.call("HGET",k,"ordinal")~=integer(item.ordinal) or redis.call("HGET",k,"funding_scope")~=scope then return redis.error_reply("FUNDING_CONFLICT usage item") end
 end
 local cap=item.reserved_micro_cents
 -- Released terminal capacity belongs to other requests. Refunds may reduce the
 -- original charge; a later increase must be recorded as unfunded, not reclaimed.
 if wasFinal and not duplicate then cap=tonumber(redis.call("HGET",k,"settled_micro_cents") or previousAmounts[i] or "0") end
 if duplicate then cap=tonumber(redis.call("HGET",k,"settled_micro_cents") or previousAmounts[i] or integer(item.settled_micro_cents)) end
 local used=math.min(remaining,cap);remaining=remaining-used;itemAmounts[i]=used;itemCaps[i]=cap
end
-- XADD is the only potentially failing write and runs before any mirror update.
-- Duplicate events can rebuild an evicted hash without adding another queue row.
if not duplicate then redis.call("XADD",KEYS[3],"*","event",ARGV[1]) end
local cents=math.floor(actual/tonumber(ARGV[10]))
local settledItems={};for i,value in ipairs(itemAmounts) do settledItems[i]=integer(value) end
local settledItemsJSON=cjson.encode(settledItems)
if oldVersion<=tonumber(ARGV[9]) then redis.call("HSET",auth,"snapshot",ARGV[2],"version",ARGV[9],"upper_bound_micro_cents",integer(a.upper_bound_micro_cents)) end
redis.call("HSET",auth,"scope",scope,"funding_settlement_revision",ARGV[4],"funding_settlement_signature",ARGV[11],"funding_settled_items",settledItemsJSON,"funding_final",final,"actual_cents",integer(cents))
redis.call("HSET",req,"funding_authorization_id",ARGV[7],"funding_scope",scope,"request_id",ARGV[8],"funding_version",integer(mirrorVersion),"funding_settlement_revision",ARGV[4],"funding_settlement_signature",ARGV[11],"funding_settled_items",settledItemsJSON,"actual_micro_cents",ARGV[5],"actual_cents",integer(cents),"funding_final",final,"status","funding_settled")
-- A delayed report must not overwrite a newer allocation's item capacities.
if oldVersion<=tonumber(ARGV[9]) then
 for i,item in ipairs(a.items) do redis.call("HSET",KEYS[i+3],"item_id",integer(item.item_id),"ordinal",integer(item.ordinal),"entitlement_id",integer(item.entitlement_id),"funding_scope",scope,"funding_final",final,"source",item.source,"settled_micro_cents",integer(itemAmounts[i]),"reserved_micro_cents",integer(final=="1" and 0 or itemCaps[i]-itemAmounts[i])) end
end
redis.call("HSET",req,"unfunded_micro_cents",integer(remaining))
return cents
`)
