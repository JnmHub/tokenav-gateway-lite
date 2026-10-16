package gatewaylite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type BindingRemote interface {
	Probe(context.Context, string) error
	Verify(context.Context, string, string) error
	Register(context.Context, string, BindingDestination) (int64, error)
	Handshake(context.Context, BindingDestination) (int64, error)
	Drain(context.Context, BindingDestination) (map[string]int64, error)
	Disconnect(context.Context, BindingDestination, string) error
	DisconnectStatus(context.Context, BindingDestination, string) (bool, error)
}
type HTTPBindingRemote struct{}

func bindingURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("binding_url_invalid")
	}
	return raw, nil
}
func validateBindingInput(in BindingInput) error {
	if _, err := bindingURL(in.URL); err != nil {
		return err
	}
	// Reject addresses during the wizard that the next generation's media
	// constructor would reject, instead of persisting an unusable public origin.
	if _, err := bindingURL(in.PublicURL); err != nil {
		return errors.New("binding_public_url_invalid")
	}
	if err := config.ValidateGatewayLiteMediaProxyPublicURL(in.PublicURL); err != nil {
		return errors.New("binding_public_url_invalid")
	}
	if strings.TrimSpace(in.Token) == "" || strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || strings.TrimSpace(in.Region) == "" || len(in.Region) > 64 {
		return errors.New("binding_node_information_required")
	}
	return nil
}
func bindingRequest(ctx context.Context, base, path, token string, body any, out any) error {
	base, err := bindingURL(base)
	if err != nil {
		return err
	}
	var encoded []byte
	method := http.MethodGet
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Credentials must never be forwarded through a redirect to another origin.
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("binding_control_plane_unreachable")
	}
	defer resp.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return errors.New("binding_response_invalid")
	}
	if json.Unmarshal(data, &result) != nil {
		return errors.New("binding_response_invalid")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !result.OK {
		switch result.Error {
		case "unauthorized", "binding_node_conflict", "binding_scope_mismatch", "binding_node_inactive", "binding_token_not_configured":
			return errors.New(result.Error)
		}
		return errors.New("binding_control_plane_rejected")
	}
	if out != nil {
		if json.Unmarshal(data, out) != nil {
			return errors.New("binding_response_invalid")
		}
	}
	return nil
}
func (HTTPBindingRemote) Probe(ctx context.Context, base string) error {
	var result struct {
		Protocol string `json:"protocol"`
	}
	if err := bindingRequest(ctx, base, "/internal/gateway-binding/capabilities", "", nil, &result); err != nil {
		return err
	}
	if result.Protocol != "tokenav-binding-v1" {
		return errors.New("binding_capability_not_supported")
	}
	return nil
}
func (HTTPBindingRemote) Verify(ctx context.Context, base, token string) error {
	return bindingRequest(ctx, base, "/internal/gateway-binding/verify", token, map[string]any{}, nil)
}
func (HTTPBindingRemote) Register(ctx context.Context, token string, d BindingDestination) (int64, error) {
	var out struct {
		NodeID int64 `json:"node_id"`
	}
	err := bindingRequest(ctx, d.URL, "/internal/gateway-binding/register", token, d, &out)
	return out.NodeID, err
}
func (HTTPBindingRemote) Handshake(ctx context.Context, d BindingDestination) (int64, error) {
	var out struct {
		NodeID    int64  `json:"node_id"`
		BindingID string `json:"binding_id"`
	}
	path := "/internal/gateway-binding/handshake"
	if d.Legacy {
		path = "/internal/gateway-binding/legacy-handshake"
	}
	err := bindingRequest(ctx, d.URL, path, d.Credential, map[string]any{"binding_id": d.BindingID, "gateway_code": d.GatewayCode, "node_scope": d.NodeScope}, &out)
	if err == nil && !d.Legacy && out.BindingID != d.BindingID {
		err = errors.New("binding_scope_mismatch")
	}
	return out.NodeID, err
}
func (HTTPBindingRemote) Drain(ctx context.Context, d BindingDestination) (map[string]int64, error) {
	var out struct {
		Outstanding map[string]int64 `json:"outstanding"`
	}
	path := "/internal/gateway-binding/handshake"
	if d.Legacy {
		path = "/internal/gateway-binding/legacy-handshake"
	}
	err := bindingRequest(ctx, d.URL, path, d.Credential, map[string]any{"binding_id": d.BindingID, "gateway_code": d.GatewayCode, "node_scope": d.NodeScope}, &out)
	if err == nil && out.Outstanding == nil {
		err = errors.New("binding_settlement_check_failed")
	}
	return out.Outstanding, err
}
func (HTTPBindingRemote) Disconnect(ctx context.Context, d BindingDestination, operation string) error {
	path := "/internal/gateway-binding/disconnect"
	if d.Legacy {
		path = "/internal/gateway-binding/legacy-disconnect"
	}
	return bindingRequest(ctx, d.URL, path, d.Credential, map[string]any{"binding_id": d.BindingID, "gateway_code": d.GatewayCode, "node_scope": d.NodeScope, "operation_id": operation}, nil)
}
func (HTTPBindingRemote) DisconnectStatus(ctx context.Context, d BindingDestination, operation string) (bool, error) {
	var out struct {
		Detached  bool   `json:"detached"`
		Operation string `json:"operation_id"`
		BindingID string `json:"binding_id"`
	}
	path := "/internal/gateway-binding/disconnect-status"
	if d.Legacy {
		path = "/internal/gateway-binding/legacy-disconnect-status"
	}
	err := bindingRequest(ctx, d.URL, path, d.Credential, map[string]any{"binding_id": d.BindingID, "gateway_code": d.GatewayCode, "node_scope": d.NodeScope, "operation_id": operation}, &out)
	if err == nil && (out.Operation != operation || out.BindingID != d.BindingID) {
		err = errors.New("binding_scope_mismatch")
	}
	return out.Detached, err
}
