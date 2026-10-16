package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

type imageTaskExecutionContextKey struct{}

// ImageTaskExecutionCapture is shared only between one background executor and
// the synchronous usage recorder it invokes. It lets the durable worker know
// that the final correction entered the billing outbox before it marks a task
// settled; no API key or upstream credential is stored in this object.
type ImageTaskExecutionCapture struct {
	mu          sync.Mutex
	done        chan struct{}
	completed   bool
	actualCents int64
	imageCount  int
	err         error
	usage       *ImageTaskUsageSnapshot
}

type imageTaskExecutionContext struct {
	snapshot ImageTaskBillingSnapshot
	capture  *ImageTaskExecutionCapture
}

func ContextWithImageTaskExecution(ctx context.Context, snapshot ImageTaskBillingSnapshot) (context.Context, *ImageTaskExecutionCapture) {
	if ctx == nil {
		ctx = context.Background()
	}
	capture := &ImageTaskExecutionCapture{done: make(chan struct{})}
	value := imageTaskExecutionContext{snapshot: snapshot, capture: capture}
	return context.WithValue(ctx, imageTaskExecutionContextKey{}, value), capture
}

func ImageTaskExecutionFromContext(ctx context.Context) (ImageTaskBillingSnapshot, *ImageTaskExecutionCapture, bool) {
	if ctx == nil {
		return ImageTaskBillingSnapshot{}, nil, false
	}
	value, ok := ctx.Value(imageTaskExecutionContextKey{}).(imageTaskExecutionContext)
	return value.snapshot, value.capture, ok && value.capture != nil
}

func (c *ImageTaskExecutionCapture) Complete(actualCents int64, imageCount int, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.completed {
		return
	}
	c.completed = true
	c.actualCents = actualCents
	c.imageCount = imageCount
	c.err = err
	close(c.done)
}

// CaptureUsage freezes only the non-secret facts required to retry RecordUsage.
// It is called before the usage job is submitted so a full worker queue or a
// process interruption can never leave a successful upstream response without
// a durable, replayable billing description.
func (c *ImageTaskExecutionCapture) CaptureUsage(snapshot *ImageTaskUsageSnapshot) {
	if c == nil || snapshot == nil {
		return
	}
	c.mu.Lock()
	c.usage = snapshot.Clone()
	c.mu.Unlock()
}

func (c *ImageTaskExecutionCapture) Result() (actualCents int64, imageCount int, completed bool, err error) {
	if c == nil {
		return 0, 0, false, errors.New("image task usage capture is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.actualCents, c.imageCount, c.completed, c.err
}

func (c *ImageTaskExecutionCapture) Wait(ctx context.Context) (actualCents int64, imageCount int, completed bool, err error) {
	if c == nil {
		return 0, 0, false, errors.New("image task usage capture is unavailable")
	}
	c.mu.Lock()
	done := c.done
	completed = c.completed
	c.mu.Unlock()
	if !completed {
		select {
		case <-ctx.Done():
			return 0, 0, false, ctx.Err()
		case <-done:
		}
	}
	return c.Result()
}

func (c *ImageTaskExecutionCapture) UsageSnapshot() *ImageTaskUsageSnapshot {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage.Clone()
}

// ImageTaskBillingSnapshot freezes the customer-facing price at submission.
// Price catalog or group changes while the task is queued must not reprice an
// already accepted job.
type ImageTaskBillingSnapshot struct {
	BillingModel        string                       `json:"billing_model"`
	ImageSize           string                       `json:"image_size"`
	RateMultiplier      float64                      `json:"rate_multiplier"`
	ImageRateMultiplier float64                      `json:"image_rate_multiplier"`
	RatePolicyAudit     *gatewaylite.RatePolicyAudit `json:"rate_policy_audit,omitempty"`
	UnitTotalCost       float64                      `json:"unit_total_cost"`
	UnitActualCost      float64                      `json:"unit_actual_cost"`
}

func (s *ImageTaskBillingSnapshot) CostFor(imageCount int) *CostBreakdown {
	if s == nil || imageCount <= 0 || s.UnitTotalCost < 0 || s.UnitActualCost < 0 ||
		math.IsNaN(s.UnitTotalCost) || math.IsNaN(s.UnitActualCost) ||
		math.IsInf(s.UnitTotalCost, 0) || math.IsInf(s.UnitActualCost, 0) {
		return nil
	}
	return &CostBreakdown{
		TotalCost: s.UnitTotalCost * float64(imageCount), ActualCost: s.UnitActualCost * float64(imageCount),
		BillingMode: string(BillingModeImage),
	}
}

func (s *OpenAIGatewayService) PrepareImageTaskBilling(ctx context.Context, apiKey *APIKey, billingModel, imageSize string, imageCount int) (*ImageTaskBillingSnapshot, int64, error) {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.User == nil || imageCount <= 0 {
		return nil, 0, errors.New("image task pricing context is unavailable")
	}
	billingModel = strings.TrimSpace(billingModel)
	if billingModel == "" {
		return nil, 0, errors.New("image task billing model is required")
	}
	apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil && resolved.Mode == BillingModeToken {
		return nil, 0, errors.New("token-billed image models do not have a safe asynchronous upper bound")
	}
	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	var ratePolicyAudit *gatewaylite.RatePolicyAudit
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		multiplier = policy.EffectiveRateMultiplier
		cloned := policy.Clone()
		ratePolicyAudit = &cloned
	} else if apiKey.GroupID != nil && apiKey.Group != nil {
		resolver := s.userGroupRateResolver
		if resolver == nil {
			resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.image_task_pricing")
		}
		resolved, err := resolver.Resolve(ctx, apiKey.User.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
		if err != nil {
			return nil, 0, err
		}
		multiplier = resolved
	}
	imageMultiplier := resolveImageRateMultiplier(apiKey, multiplier)
	tier := NormalizeImageBillingTierOrDefault(imageSize)
	one := s.calculateOpenAIImageCost(ctx, billingModel, apiKey, &OpenAIForwardResult{
		Model: billingModel, BillingModel: billingModel, UpstreamModel: billingModel,
		ImageCount: 1, ImageSize: tier,
	}, imageMultiplier)
	if one == nil || one.ActualCost < 0 || math.IsNaN(one.ActualCost) || math.IsInf(one.ActualCost, 0) {
		return nil, 0, errors.New("image task price cannot be resolved")
	}
	snapshot := &ImageTaskBillingSnapshot{
		BillingModel: billingModel, ImageSize: tier, RateMultiplier: multiplier,
		ImageRateMultiplier: imageMultiplier, RatePolicyAudit: ratePolicyAudit,
		UnitTotalCost: one.TotalCost, UnitActualCost: one.ActualCost,
	}
	maximum := snapshot.CostFor(imageCount)
	if maximum == nil {
		return nil, 0, errors.New("image task maximum price cannot be resolved")
	}
	estimatedCents := int64(math.Ceil(maximum.ActualCost*100 - 1e-9))
	if estimatedCents <= 0 {
		estimatedCents = 1
	}
	if gatewaylite.HasRequestFunding(ctx) {
		bound,err:=s.FundingOpenAIImageUpperBound(ctx,apiKey,billingModel,imageCount)
		if err!=nil{return nil,0,err}
		// The hold covers every reported size; actual billing keeps the frozen quote.
		capacityCents:=(bound.MicroCents+999_999)/1_000_000
		if capacityCents>estimatedCents {estimatedCents=capacityCents}
	}
	return snapshot, estimatedCents, nil
}
