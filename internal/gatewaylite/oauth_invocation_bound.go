package gatewaylite

import (
	"context"
	"encoding/json"
)

type oauthInvocationBoundProofKey struct{}

// ContextWithOAuthInvocationBoundProof carries the card service's price/limit
// proof through initial authorize and subsequent resize. This contains no price
// calculator: the bound and evidence both come from the actual billing resolver.
func ContextWithOAuthInvocationBoundProof(ctx context.Context, proof map[string]any) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, oauthInvocationBoundProofKey{}, cloneOAuthInvocationProof(proof))
}

func oauthInvocationBoundProofFromContext(ctx context.Context) map[string]any {
	if ctx == nil {
		return nil
	}
	proof, _ := ctx.Value(oauthInvocationBoundProofKey{}).(map[string]any)
	return cloneOAuthInvocationProof(proof)
}

func cloneOAuthInvocationProof(proof map[string]any) map[string]any {
	if proof == nil {
		return nil
	}
	body, err := json.Marshal(proof)
	if err != nil {
		return nil
	}
	var copied map[string]any
	if json.Unmarshal(body, &copied) != nil {
		return nil
	}
	return copied
}
