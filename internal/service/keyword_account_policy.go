package service

import "context"

type keywordProtectedAccountIDsContextKey struct{}

// MergeKeywordProtectedAccountIDs returns an independent set so retry code may
// append failed account IDs without mutating the request's immutable policy.
func MergeKeywordProtectedAccountIDs(ctx context.Context, excludedIDs map[int64]struct{}) map[int64]struct{} {
	protected := KeywordProtectedAccountIDsFromContext(ctx)
	if len(protected) == 0 && len(excludedIDs) == 0 {
		return nil
	}
	merged := make(map[int64]struct{}, len(protected)+len(excludedIDs))
	for id := range protected {
		merged[id] = struct{}{}
	}
	for id := range excludedIDs {
		merged[id] = struct{}{}
	}
	return merged
}

func ContextWithKeywordProtectedAccountIDs(ctx context.Context, accountIDs map[int64]struct{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(accountIDs) == 0 {
		return ctx
	}
	cloned := make(map[int64]struct{}, len(accountIDs))
	for id := range accountIDs {
		if id > 0 {
			cloned[id] = struct{}{}
		}
	}
	if len(cloned) == 0 {
		return ctx
	}
	return context.WithValue(ctx, keywordProtectedAccountIDsContextKey{}, cloned)
}

func KeywordProtectedAccountIDsFromContext(ctx context.Context) map[int64]struct{} {
	if ctx == nil {
		return nil
	}
	stored, _ := ctx.Value(keywordProtectedAccountIDsContextKey{}).(map[int64]struct{})
	if len(stored) == 0 {
		return nil
	}
	cloned := make(map[int64]struct{}, len(stored))
	for id := range stored {
		cloned[id] = struct{}{}
	}
	return cloned
}

func keywordPolicyExcludedAccountIDs(accounts []Account, groupAllows bool) map[int64]struct{} {
	var excluded map[int64]struct{}
	for index := range accounts {
		account := &accounts[index]
		if account.ID <= 0 || account.AcceptsBlockedKeywords(groupAllows) {
			continue
		}
		if excluded == nil {
			excluded = make(map[int64]struct{})
		}
		excluded[account.ID] = struct{}{}
	}
	return excluded
}

// AccountAcceptsBlockedKeywords resolves an already-bound long-lived account
// once per connection. Handlers cache the result instead of reading account
// state for every WebSocket frame.
func (s *OpenAIGatewayService) AccountAcceptsBlockedKeywords(ctx context.Context, accountID int64, groupAllows bool) (bool, error) {
	if s == nil || accountID <= 0 {
		return false, nil
	}
	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		return false, err
	}
	return account.AcceptsBlockedKeywords(groupAllows), nil
}
