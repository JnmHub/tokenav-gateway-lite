//go:build unit

package service

import (
	"testing"
	"time"
)

func TestNormalizeWeightedModelMapping_EmptyKeepsDisabled(t *testing.T) {
	out, err := NormalizeWeightedModelMapping(PlatformOpenAI, map[string]string{}, nil)
	if err != nil {
		t.Fatalf("empty input should pass: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty mapping, got %v", out)
	}
}

func TestNormalizeWeightedModelMapping_OnlyOpenAIGroups(t *testing.T) {
	input := map[string][]WeightedModelTarget{
		"pro-fast": {
			{Model: "gpt-5.6-sol", Weight: 70},
			{Model: "gpt-5.6-terra", Weight: 30},
		},
	}
	if _, err := NormalizeWeightedModelMapping(PlatformAnthropic, nil, input); err == nil {
		t.Fatal("anthropic group should reject weighted mapping")
	}
	if _, err := NormalizeWeightedModelMapping(PlatformOpenAI, nil, input); err != nil {
		t.Fatalf("openai group should accept: %v", err)
	}
}

func TestNormalizeWeightedModelMapping_Rules(t *testing.T) {
	base := func() map[string][]WeightedModelTarget {
		return map[string][]WeightedModelTarget{
			"pro-fast": {
				{Model: "gpt-5.6-sol", Weight: 70},
				{Model: "gpt-5.6-terra", Weight: 30},
			},
		}
	}
	cases := []struct {
		name    string
		mutate  func(input map[string][]WeightedModelTarget)
		single  map[string]string
		wantErr bool
	}{
		{
			name: "single target rejected",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["pro-fast"] = in["pro-fast"][:1]
			},
			wantErr: true,
		},
		{
			name: "key overlaps single mapping",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["gpt-5.6-sol"] = []WeightedModelTarget{
					{Model: "gpt-5.6-terra", Weight: 1},
					{Model: "gpt-5.6-luna", Weight: 1},
				}
			},
			single:  map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"},
			wantErr: true,
		},
		{
			// 分流目标是终端上游模型（单跳），与单目标映射的公开键同名
			// 不构成链路：直接请求该键走单目标，分流命中它则按它本身选号。
			name: "target sharing a single mapping key is allowed",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["pro-fast"] = []WeightedModelTarget{
					{Model: "other-virtual", Weight: 1},
					{Model: "gpt-5.6-luna", Weight: 1},
				}
			},
			single:  map[string]string{"gpt-5.6-luna": "gpt-5.6-sol"},
			wantErr: false,
		},
		{
			// 目标命中另一个分流键同理：命中即终端，不会级联再分流。
			name: "target sharing another weighted key is allowed",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["other-virtual"] = []WeightedModelTarget{
					{Model: "gpt-5.6-sol", Weight: 1},
					{Model: "gpt-5.6-terra", Weight: 1},
				}
			},
			wantErr: false,
		},
		{
			name: "target crosses provider",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["pro-fast"] = []WeightedModelTarget{
					{Model: "claude-fable-5", Weight: 1},
					{Model: "gpt-5.6-luna", Weight: 1},
				}
			},
			wantErr: true,
		},
		{
			name: "weight out of range",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["pro-fast"][0].Weight = 0
			},
			wantErr: true,
		},
		{
			name: "wildcard key rejected",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["gpt-*"] = []WeightedModelTarget{
					{Model: "gpt-5.6-sol", Weight: 1},
					{Model: "gpt-5.6-terra", Weight: 1},
				}
			},
			wantErr: true,
		},
		{
			name: "self target rejected",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["pro-fast"][1].Model = "pro-fast"
			},
			wantErr: true,
		},
		{
			name: "duplicate target rejected",
			mutate: func(in map[string][]WeightedModelTarget) {
				in["pro-fast"][1].Model = "GPT-5.6-SOL"
			},
			wantErr: true,
		},
		{
			name: "valid mapping passes",
			mutate: func(in map[string][]WeightedModelTarget) {},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base()
			tc.mutate(input)
			_, err := NormalizeWeightedModelMapping(PlatformOpenAI, tc.single, input)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGroupWeightedModelTargetsFor(t *testing.T) {
	group := &Group{
		ID:                  7,
		Platform:            PlatformOpenAI,
		WeightedModelMapping: map[string][]WeightedModelTarget{
			"pro-fast": {
				{Model: "gpt-5.6-sol", Weight: 70},
				{Model: "gpt-5.6-terra", Weight: 30},
			},
		},
	}
	if _, ok := group.WeightedModelTargetsFor("PRO-FAST"); !ok {
		t.Fatal("lookup should be case-insensitive")
	}
	if _, ok := group.WeightedModelTargetsFor("gpt-5.6-sol"); ok {
		t.Fatal("unconfigured model must not match")
	}
}

func TestPickWeightedModelTarget_Distribution(t *testing.T) {
	targets := []WeightedModelTarget{
		{Model: "a", Weight: 70},
		{Model: "b", Weight: 30},
	}
	counts := map[string]int{}
	for i := 0; i < 1000; i++ {
		picked, ok := pickWeightedModelTarget(901, "dist-model", targets, nil)
		if !ok {
			t.Fatal("pick should succeed")
		}
		counts[picked]++
	}
	if counts["a"] < 600 || counts["a"] > 800 {
		t.Fatalf("weight 70 target picked %d/1000 times, want ~700", counts["a"])
	}
	if counts["b"] < 200 || counts["b"] > 400 {
		t.Fatalf("weight 30 target picked %d/1000 times, want ~300", counts["b"])
	}
}

func TestPickWeightedModelTarget_ExcludeAndFailOpen(t *testing.T) {
	targets := []WeightedModelTarget{
		{Model: "a", Weight: 50},
		{Model: "b", Weight: 50},
	}
	// 排除 a 后只剩 b。
	for i := 0; i < 10; i++ {
		picked, ok := pickWeightedModelTarget(902, "ex-model", targets, map[string]struct{}{"a": {}})
		if !ok || picked != "b" {
			t.Fatalf("expected b, got %q ok=%v", picked, ok)
		}
	}
	// 全部冷却：fail-open 仍可选（宁试降级目标不拒绝）。
	RecordModelRouteFailure(902, "ex-model", "a", ModelRouteSignalModelNotFound)
	RecordModelRouteFailure(902, "ex-model", "b", ModelRouteSignalModelNotFound)
	defer ResetModelRouteHealth(902, "ex-model", "a")
	defer ResetModelRouteHealth(902, "ex-model", "b")
	picked, ok := pickWeightedModelTarget(902, "ex-model", targets, nil)
	if !ok {
		t.Fatal("all-cooling must fail open")
	}
	if picked != "a" && picked != "b" {
		t.Fatalf("unexpected target %q", picked)
	}
}

func TestPickWeightedModelTarget_CoolingTargetAvoided(t *testing.T) {
	targets := []WeightedModelTarget{
		{Model: "a", Weight: 99},
		{Model: "b", Weight: 1},
	}
	RecordModelRouteFailure(903, "cool-model", "a", ModelRouteSignalModelNotFound)
	defer ResetModelRouteHealth(903, "cool-model", "a")
	for i := 0; i < 20; i++ {
		picked, _ := pickWeightedModelTarget(903, "cool-model", targets, nil)
		// 权重 99 的 a 在冷却：要么选 b，要么拿到 a 的半开探测租约（fail-open
		// 只在全部冷却时发生，b 仍健康，所以不允许选 a）。
		if picked == "a" {
			t.Fatalf("cooling target a must not be picked while b is healthy (iter %d)", i)
		}
	}
}

func TestResolveGroupModelForSelection_WeightedPick(t *testing.T) {
	group := &Group{
		ID:       904,
		Platform: PlatformOpenAI,
		WeightedModelMapping: map[string][]WeightedModelTarget{
			"pro-fast": {
				{Model: "gpt-5.6-sol", Weight: 50},
				{Model: "gpt-5.6-terra", Weight: 50},
			},
		},
		ModelMapping: map[string]string{"gpt-fast-*": "gpt-5.6-sol"},
	}
	ctx, resolved := resolveGroupModelForSelection(t.Context(), group, "pro-fast")
	if resolved != "gpt-5.6-sol" && resolved != "gpt-5.6-terra" {
		t.Fatalf("unexpected resolved model %q", resolved)
	}
	// 传播链必须生效：分组兜底选择结构携带当前目标，转发层据此改写请求体。
	if _, ok := openAIGroupModelFallbackTargetForModel(ctx, group.ID, "pro-fast"); !ok {
		t.Fatal("forward override ctx should expose the weighted target")
	}
	// 精确分流优先于单目标通配符：同键带通配符的单目标映射不得抢走分流。
	if target, _ := group.ResolveModelMapping("pro-fast"); target != "pro-fast" {
		t.Fatalf("single wildcard must not shadow weighted exact key, got %q", target)
	}
}

func TestClassifyModelRouteFailureSignal(t *testing.T) {
	if got := ClassifyModelRouteFailureSignal(404, nil); got != ModelRouteSignalModelNotFound {
		t.Fatalf("404 should be model_not_found, got %s", got)
	}
	if got := ClassifyModelRouteFailureSignal(503, nil); got != ModelRouteSignalGeneric {
		t.Fatalf("503 should be generic, got %s", got)
	}
	if got := ClassifyModelRouteFailureSignal(429, nil); got != ModelRouteSignalAuth {
		t.Fatalf("429 should be auth (account-scoped), got %s", got)
	}
	if got := ClassifyModelRouteFailureSignal(401, nil); got != ModelRouteSignalAuth {
		t.Fatalf("401 should be auth, got %s", got)
	}
}

func TestModelRouteHealth_Lifecycle(t *testing.T) {
	const groupID int64 = 905
	const public = "life-model"
	health := newModelRouteHealthState(64)
	now := time.Now()

	// 单次泛化失败：只记串不禁用。
	if health.recordFailure(groupID, public, "t1", ModelRouteSignalGeneric, now) {
		t.Fatal("first generic failure should not cool down")
	}
	// 第二次：进入短冷却。
	if !health.recordFailure(groupID, public, "t1", ModelRouteSignalGeneric, now.Add(time.Minute)) {
		t.Fatal("second consecutive failure should cool down")
	}
	// 成功清零。
	health.recordSuccess(groupID, public, "t1", now.Add(2*time.Minute))
	views := health.snapshot(groupID, now.Add(2*time.Minute))
	if len(views) != 1 || views[0].State != ModelRouteHealthHealthy || views[0].ConsecutiveFail != 0 {
		t.Fatalf("success should reset entry, got %+v", views)
	}
	// 404 硬冷却 30 分钟。
	if !health.recordFailure(groupID, public, "t2", ModelRouteSignalModelNotFound, now.Add(3*time.Minute)) {
		t.Fatal("model_not_found should cool down immediately")
	}
	views = health.snapshot(groupID, now.Add(3*time.Minute))
	for _, view := range views {
		if view.TargetModel == "t2" {
			if view.State != ModelRouteHealthCooling || view.CooldownRemaining <= 0 {
				t.Fatalf("t2 should be cooling, got %+v", view)
			}
		}
	}
	// 手动重置。
	if !health.reset(groupID, public, "t2") {
		t.Fatal("reset should succeed for existing entry")
	}
	views = health.snapshot(groupID, now.Add(3*time.Minute))
	for _, view := range views {
		if view.TargetModel == "t2" && view.State != ModelRouteHealthHealthy {
			t.Fatalf("t2 should be healthy after reset, got %+v", view)
		}
	}
	// 账号级信号不记录。
	if health.recordFailure(groupID, public, "t3", ModelRouteSignalAuth, now.Add(4*time.Minute)) {
		t.Fatal("auth signal must be ignored")
	}
	if views := health.snapshot(groupID, now.Add(4*time.Minute)); len(views) != 2 {
		t.Fatalf("auth signal must not create entry, got %+v", views)
	}
}

func TestModelRouteHealth_HalfOpenProbeLease(t *testing.T) {
	const groupID int64 = 906
	const public = "probe-model"
	health := newModelRouteHealthState(64)
	now := time.Now()

	health.recordFailure(groupID, public, "t1", ModelRouteSignalModelNotFound, now)
	key, ok := modelRouteKeyFor(groupID, public, "t1")
	if !ok {
		t.Fatal("key should be valid")
	}
	// 冷却期内不可选。
	if health.modelRouteSelectable(key, now.Add(time.Minute)) {
		t.Fatal("target must be unselectable during cooldown")
	}
	// 冷却结束：第一个调用者拿到探测租约，第二个不行（半开）。
	afterCooldown := now.Add(31 * time.Minute)
	if !health.modelRouteSelectable(key, afterCooldown) {
		t.Fatal("first caller after cooldown should get the probe lease")
	}
	if health.modelRouteSelectable(key, afterCooldown.Add(time.Second)) {
		t.Fatal("second caller must not get another probe lease")
	}
	// 探测成功后恢复可选。
	health.recordSuccess(groupID, public, "t1", afterCooldown.Add(2*time.Second))
	if !health.modelRouteSelectable(key, afterCooldown.Add(3*time.Second)) {
		t.Fatal("target should be selectable again after successful probe")
	}
}

func TestModelRouteHealth_BackoffDoublesAfterFailedProbe(t *testing.T) {
	const groupID int64 = 907
	const public = "backoff-model"
	health := newModelRouteHealthState(64)
	now := time.Now()
	key, ok := modelRouteKeyFor(groupID, public, "t1")
	if !ok {
		t.Fatal("key should be valid")
	}

	// 连续两次泛化失败进入 10 秒短冷却。
	health.recordFailure(groupID, public, "t1", ModelRouteSignalGeneric, now)
	if !health.recordFailure(groupID, public, "t1", ModelRouteSignalGeneric, now.Add(time.Second)) {
		t.Fatal("streak of two should cool down")
	}
	// 冷却结束：拿到半开探测租约。
	afterCooldown := now.Add(12 * time.Second)
	if !health.modelRouteSelectable(key, afterCooldown) {
		t.Fatal("first caller after cooldown should get the probe lease")
	}
	// 探测失败（租约期内）：退避至少翻倍到 20 秒。
	if !health.recordFailure(groupID, public, "t1", ModelRouteSignalGeneric, afterCooldown.Add(time.Second)) {
		t.Fatal("failed probe should cool down again")
	}
	views := health.snapshot(groupID, afterCooldown.Add(time.Second))
	for _, view := range views {
		if view.TargetModel == "t1" && view.CooldownRemaining <= 10*1000 {
			t.Fatalf("cooldown should double after failed probe, got %dms", view.CooldownRemaining)
		}
	}
}
