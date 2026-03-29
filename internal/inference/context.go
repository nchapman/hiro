package inference

import "context"

type contextKey int

const (
	callerIDKey contextKey = iota
	callChainKey
)

// ContextWithCallerID returns a context with the caller instance ID set.
func ContextWithCallerID(ctx context.Context, callerID string) context.Context {
	return context.WithValue(ctx, callerIDKey, callerID)
}

// callerIDFromContext returns the caller instance ID from the context.
func callerIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(callerIDKey).(string)
	return id
}

// ContextWithCallChain returns a context with the given instance added to the call chain.
// Used to detect re-entrant deadlocks in coordinator tools.
func ContextWithCallChain(ctx context.Context, instanceID string) context.Context {
	chain := callChainFromContext(ctx)
	newChain := make(map[string]bool, len(chain)+1)
	for k := range chain {
		newChain[k] = true
	}
	newChain[instanceID] = true
	return context.WithValue(ctx, callChainKey, newChain)
}

// callChainFromContext returns the set of instances currently in the call chain.
func callChainFromContext(ctx context.Context) map[string]bool {
	chain, _ := ctx.Value(callChainKey).(map[string]bool)
	return chain
}

// IsInCallChain returns true if the instance is already in the call chain,
// indicating a potential deadlock from re-entrant coordinator tool calls.
func IsInCallChain(ctx context.Context, instanceID string) bool {
	chain := callChainFromContext(ctx)
	return chain[instanceID]
}
