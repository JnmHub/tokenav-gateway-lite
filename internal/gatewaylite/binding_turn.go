package gatewaylite

import (
	"context"
	"errors"
)

type bindingTurnKey struct{}

var ErrBindingDraining = errors.New("网关正在完成旧主站结算，请稍后重新连接")

func ContextWithBindingTurnGuard(ctx context.Context, m *BindingManager) context.Context {
	return context.WithValue(ctx, bindingTurnKey{}, m)
}
func CheckBindingTurn(ctx context.Context) error {
	m, _ := ctx.Value(bindingTurnKey{}).(*BindingManager)
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.record.Phase == "bound" || (m.record.Phase == "activating" && m.record.Current != nil && m.record.Current.Legacy) {
		return nil
	}
	return ErrBindingDraining
}

// Existing funding/agent closures remain intact. Each newly admitted turn reads
// its own connection's manager, never the global manager of a later generation.
func wrapBindingTurnAdmission(ctx context.Context, admit TurnAdmission) TurnAdmission {
	m, _ := ctx.Value(bindingTurnKey{}).(*BindingManager)
	if m == nil {
		return admit
	}
	return func(turnCtx context.Context, id, model string) (RequestMeta, error) {
		if err := CheckBindingTurn(ContextWithBindingTurnGuard(turnCtx, m)); err != nil {
			return RequestMeta{}, err
		}
		return admit(turnCtx, id, model)
	}
}
