package gatewaylite

import "context"

func (c *ControlPlaneClient) ResolveOAuthInvocation(ctx context.Context, req OAuthInvocationResolveRequest) (*OAuthInvocationResolveResponse, error) {
	var out OAuthInvocationResolveResponse
	err := c.postJSON(ctx, "/internal/oauth/invocations/resolve", req, &out)
	return &out, err
}

func (c *ControlPlaneClient) ReserveOAuthInvocation(ctx context.Context, req OAuthInvocationReserveRequest) (*OAuthInvocationReservationResponse, error) {
	var out OAuthInvocationReservationResponse
	err := c.postJSON(ctx, "/internal/oauth/invocations/reserve", req, &out)
	return &out, err
}

func (c *ControlPlaneClient) BindOAuthInvocation(ctx context.Context, req OAuthInvocationBindRequest) (*OAuthInvocationReservationResponse, error) {
	var out OAuthInvocationReservationResponse
	err := c.postJSON(ctx, "/internal/oauth/invocations/bind", req, &out)
	return &out, err
}

func (c *ControlPlaneClient) ResizeOAuthInvocation(ctx context.Context, req OAuthInvocationResizeRequest) (*OAuthInvocationReservationResponse, error) {
	var out OAuthInvocationReservationResponse
	err := c.postJSON(ctx, "/internal/oauth/invocations/resize", req, &out)
	return &out, err
}

func (c *ControlPlaneClient) SettleOAuthInvocation(ctx context.Context, req OAuthInvocationFinalizeRequest) (*OAuthInvocationReservationResponse, error) {
	var out OAuthInvocationReservationResponse
	// This is a durable usage worker, not a cold-cache auth call. Keep the existing
	// usage timeout so financial replay shares the queue's bounded time budget.
	err := c.postJSONWithClient(ctx, c.usageHTTPClient, "/internal/oauth/invocations/settle", req, &out)
	return &out, err
}

func (c *ControlPlaneClient) ReleaseOAuthInvocation(ctx context.Context, req OAuthInvocationFinalizeRequest) (*OAuthInvocationReservationResponse, error) {
	var out OAuthInvocationReservationResponse
	err := c.postJSON(ctx, "/internal/oauth/invocations/release", req, &out)
	return &out, err
}

func (c *ControlPlaneClient) OAuthInvocationStatus(ctx context.Context, req OAuthInvocationStatusRequest) (*OAuthInvocationReservationResponse, error) {
	var out OAuthInvocationReservationResponse
	err := c.postJSON(ctx, "/internal/oauth/invocations/status", req, &out)
	return &out, err
}

func (r *ControlPlaneClientRef) OAuthInvocationStatus(ctx context.Context, req OAuthInvocationStatusRequest) (*OAuthInvocationReservationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.OAuthInvocationStatus(ctx, req)
}

func (r *ControlPlaneClientRef) ResolveOAuthInvocation(ctx context.Context, req OAuthInvocationResolveRequest) (*OAuthInvocationResolveResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.ResolveOAuthInvocation(ctx, req)
}

func (r *ControlPlaneClientRef) ReserveOAuthInvocation(ctx context.Context, req OAuthInvocationReserveRequest) (*OAuthInvocationReservationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.ReserveOAuthInvocation(ctx, req)
}

func (r *ControlPlaneClientRef) BindOAuthInvocation(ctx context.Context, req OAuthInvocationBindRequest) (*OAuthInvocationReservationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.BindOAuthInvocation(ctx, req)
}

func (r *ControlPlaneClientRef) ResizeOAuthInvocation(ctx context.Context, req OAuthInvocationResizeRequest) (*OAuthInvocationReservationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.ResizeOAuthInvocation(ctx, req)
}

func (r *ControlPlaneClientRef) SettleOAuthInvocation(ctx context.Context, req OAuthInvocationFinalizeRequest) (*OAuthInvocationReservationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.SettleOAuthInvocation(ctx, req)
}

func (r *ControlPlaneClientRef) ReleaseOAuthInvocation(ctx context.Context, req OAuthInvocationFinalizeRequest) (*OAuthInvocationReservationResponse, error) {
	c, err := r.current()
	if err != nil {
		return nil, err
	}
	return c.ReleaseOAuthInvocation(ctx, req)
}
