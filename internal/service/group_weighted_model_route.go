package service

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// 智能分流选择器：进程内平滑加权轮询（smooth weighted round-robin）。
//
// 选 SWRR 而非纯随机的理由：低流量时分布确定、不会把权重 70/30 抽成 100/0，
// 也避免同一时刻的并发突发集中打到同一目标。多网关实例各自独立轮询，
// 统计上仍接近配置比例；不需要全局协调。
//
// 选择时先按目标健康过滤冷却中的目标；全部冷却则 fail-open 放开全部目标
// （宁可让请求打到降级目标，也不能因为健康簿全红直接拒绝服务）。

type weightedModelRoutePicker struct {
	mu       sync.Mutex
	currents map[string]map[string]int
}

var defaultWeightedModelRoutePicker = &weightedModelRoutePicker{
	currents: make(map[string]map[string]int),
}

func weightedModelRoutePickerKey(groupID int64, publicModel string) string {
	return strings.ToLower(strings.TrimSpace(publicModel)) + "\x00" +
		strings.TrimSpace(int64ToString(groupID))
}

func int64ToString(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// pickWeightedModelTarget 从候选目标中按权重与健康状态选出一个。
// exclude 里的目标（本请求已尝试过的）直接跳过。
func pickWeightedModelTarget(groupID int64, publicModel string, targets []WeightedModelTarget, exclude map[string]struct{}) (string, bool) {
	if len(targets) == 0 {
		return "", false
	}
	now := time.Now()
	eligible := make([]WeightedModelTarget, 0, len(targets))
	for _, target := range targets {
		model := strings.TrimSpace(target.Model)
		if model == "" {
			continue
		}
		if _, excluded := exclude[strings.ToLower(model)]; excluded {
			continue
		}
		key, ok := modelRouteKeyFor(groupID, publicModel, model)
		if !ok {
			continue
		}
		if !defaultModelRouteHealth.modelRouteSelectable(key, now) {
			continue
		}
		eligible = append(eligible, WeightedModelTarget{Model: model, Weight: target.Weight})
	}
	if len(eligible) == 0 {
		// 全部冷却或被排除：fail-open 放开冷却目标（排除列表仍然生效），
		// 这是"所有目标都坏了"时的最后手段，宁试降级目标不直接拒绝。
		for _, target := range targets {
			model := strings.TrimSpace(target.Model)
			if model == "" {
				continue
			}
			if _, excluded := exclude[strings.ToLower(model)]; excluded {
				continue
			}
			eligible = append(eligible, WeightedModelTarget{Model: model, Weight: target.Weight})
		}
	}
	if len(eligible) == 0 {
		return "", false
	}
	return defaultWeightedModelRoutePicker.pick(groupID, publicModel, eligible), true
}

// pick 执行平滑加权轮询：每轮给所有候选加上自身权重，取累计值最大者，
// 再把它的累计值减去总权重。多轮之后每个目标被选中的次数比等于权重比。
func (p *weightedModelRoutePicker) pick(groupID int64, publicModel string, targets []WeightedModelTarget) string {
	if len(targets) == 1 {
		return targets[0].Model
	}
	key := weightedModelRoutePickerKey(groupID, publicModel)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.currents == nil {
		p.currents = make(map[string]map[string]int)
	}
	currents, ok := p.currents[key]
	if !ok {
		currents = make(map[string]int, len(targets))
		p.currents[key] = currents
	}

	total := 0
	best := targets[0]
	bestCurrent := 0
	for _, target := range targets {
		weight := target.Weight
		if weight <= 0 {
			weight = 1
		}
		current := currents[strings.ToLower(target.Model)] + weight
		currents[strings.ToLower(target.Model)] = current
		total += weight
		if current > bestCurrent {
			best, bestCurrent = target, current
		}
	}
	currents[strings.ToLower(best.Model)] -= total
	return best.Model
}

// orderedWeightedModelCandidates 返回除权重轮询首选外的备选顺序：
// 按权重降序、同权重按名称排序，保证"首选失败后的轮转顺序"确定可预期。
func orderedWeightedModelCandidates(targets []WeightedModelTarget, exclude map[string]struct{}) []string {
	ordered := make([]WeightedModelTarget, len(targets))
	copy(ordered, targets)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Weight != ordered[j].Weight {
			return ordered[i].Weight > ordered[j].Weight
		}
		return ordered[i].Model < ordered[j].Model
	})
	out := make([]string, 0, len(ordered))
	for _, target := range ordered {
		model := strings.TrimSpace(target.Model)
		if model == "" {
			continue
		}
		if _, excluded := exclude[strings.ToLower(model)]; excluded {
			continue
		}
		out = append(out, model)
	}
	return out
}

// ClassifyModelRouteFailureSignal 把上游故障转移错误映射为目标健康信号。
// 归因边界（错记会把健康目标误禁用，宁可漏记不可错记）：
//   - 404 / Codex plan-gated 400：模型不存在或套餐不含该模型，硬冷却；
//   - 500/502/503/504/529 与 0（网络层错误）：泛化连击信号；
//   - 401/403/429：账号级信号，账号禁用链路处理，目标健康不记。
func ClassifyModelRouteFailureSignal(statusCode int, responseBody []byte) ModelRouteFailureSignal {
	switch {
	case statusCode == 404:
		return ModelRouteSignalModelNotFound
	case statusCode == 400 && isOpenAICodexPlanGatedModelError(statusCode, responseBody):
		return ModelRouteSignalModelNotFound
	case statusCode >= 500, statusCode == 0, statusCode == 529:
		return ModelRouteSignalGeneric
	default:
		return ModelRouteSignalAuth
	}
}

// RecordWeightedModelRouteUpstreamFailure 在故障转移循环中记录当前分流目标的
// 健康失败。currentTarget 为空表示本次请求没有走分流（单目标映射/直连），
// 直接跳过；返回是否触发冷却，供调用方记日志。
func RecordWeightedModelRouteUpstreamFailure(groupID int64, publicModel, currentTarget string, statusCode int, responseBody []byte) bool {
	currentTarget = strings.TrimSpace(currentTarget)
	if groupID <= 0 || strings.TrimSpace(publicModel) == "" || currentTarget == "" {
		return false
	}
	signal := ClassifyModelRouteFailureSignal(statusCode, responseBody)
	if signal == ModelRouteSignalAuth {
		return false
	}
	return RecordModelRouteFailure(groupID, publicModel, currentTarget, signal)
}

// RecordWeightedModelRouteUpstreamSuccess 在转发成功后清除目标健康状态。
// currentTarget 为空表示没有走分流。
func RecordWeightedModelRouteUpstreamSuccess(groupID int64, publicModel, currentTarget string) {
	currentTarget = strings.TrimSpace(currentTarget)
	if groupID <= 0 || strings.TrimSpace(publicModel) == "" || currentTarget == "" {
		return
	}
	RecordModelRouteSuccess(groupID, publicModel, currentTarget)
}
