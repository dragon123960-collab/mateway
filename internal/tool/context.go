package tool

import "context"

type approvalTokenContextKey struct{}

func WithApprovalToken(ctx context.Context, token string) context.Context {
	if ctx == nil || token == "" {
		return ctx
	}
	return context.WithValue(ctx, approvalTokenContextKey{}, token)
}

func ApprovalTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(approvalTokenContextKey{}).(string)
	return token
}
