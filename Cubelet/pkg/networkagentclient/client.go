// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package networkagentclient

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
)

var ErrNotConfigured = errors.New("network-agent client is not configured")
var ErrUnsupportedEndpoint = errors.New("network-agent endpoint is unsupported")
var ErrInvalidEndpoint = errors.New("network-agent endpoint is invalid")

const enableHTTPClientEnv = "CUBELET_ENABLE_NETWORK_AGENT_HTTP_CLIENT"

type Client interface {
	EnsureNetwork(ctx context.Context, req *EnsureNetworkRequest) (*EnsureNetworkResponse, error)
	ReleaseNetwork(ctx context.Context, req *ReleaseNetworkRequest) error
	ReconcileNetwork(ctx context.Context, req *ReconcileNetworkRequest) (*ReconcileNetworkResponse, error)
	GetNetwork(ctx context.Context, req *GetNetworkRequest) (*GetNetworkResponse, error)
	ListNetworks(ctx context.Context, req *ListNetworksRequest) (*ListNetworksResponse, error)
	Health(ctx context.Context, req *HealthRequest) error
}

type noopClient struct{}

type closeableClient interface {
	Client
	close() error
}

type reconnectingClient struct {
	endpoint string
	mu       sync.RWMutex
	client   Client
}

var concreteClientFactory = newConcreteClient

func NewClient(endpoint string) (Client, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return NewNoopClient(), nil
	}
	if !isSupportedEndpoint(ep) {
		return NewNoopClient(), ErrUnsupportedEndpoint
	}
	if err := validateEndpoint(ep); err != nil {
		return NewNoopClient(), err
	}
	return &reconnectingClient{endpoint: ep}, nil
}

func newConcreteClient(endpoint string) (Client, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return NewNoopClient(), nil
	}
	if strings.HasPrefix(ep, "grpc://") || strings.HasPrefix(ep, "grpc+unix://") {
		c, err := newGRPCClient(ep)
		if err != nil {
			return NewNoopClient(), err
		}
		return c, nil
	}
	if strings.HasPrefix(ep, "http://") || strings.HasPrefix(ep, "https://") {
		if !httpClientEnabled() {
			return NewNoopClient(), ErrUnsupportedEndpoint
		}
		return newHTTPClient(ep), nil
	}
	if strings.HasPrefix(ep, "unix://") {
		if !httpClientEnabled() {
			return NewNoopClient(), ErrUnsupportedEndpoint
		}
		c, err := newUnixHTTPClient(ep)
		if err != nil {
			return NewNoopClient(), err
		}
		return c, nil
	}
	return NewNoopClient(), ErrUnsupportedEndpoint
}

func isSupportedEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "grpc://") ||
		strings.HasPrefix(endpoint, "grpc+unix://") ||
		strings.HasPrefix(endpoint, "http://") ||
		strings.HasPrefix(endpoint, "https://") ||
		strings.HasPrefix(endpoint, "unix://")
}

func validateEndpoint(endpoint string) error {
	switch {
	case strings.HasPrefix(endpoint, "grpc://"), strings.HasPrefix(endpoint, "grpc+unix://"):
		_, err := grpcTargetFromEndpoint(endpoint)
		return err
	case strings.HasPrefix(endpoint, "unix://"):
		if !httpClientEnabled() {
			return ErrUnsupportedEndpoint
		}
		if strings.TrimSpace(strings.TrimPrefix(endpoint, "unix://")) == "" {
			return ErrInvalidEndpoint
		}
		return nil
	case strings.HasPrefix(endpoint, "http://"), strings.HasPrefix(endpoint, "https://"):
		if !httpClientEnabled() {
			return ErrUnsupportedEndpoint
		}
		return nil
	default:
		return ErrUnsupportedEndpoint
	}
}

func (c *reconnectingClient) current() Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

func (c *reconnectingClient) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cc, ok := c.client.(closeableClient); ok {
		_ = cc.close()
	}
	c.client = nil
}

func (c *reconnectingClient) ensureClient() (Client, error) {
	if current := c.current(); current != nil {
		return current, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	client, err := concreteClientFactory(c.endpoint)
	if err != nil {
		return nil, err
	}
	c.client = client
	return client, nil
}

func shouldReconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNotConfigured) {
		return true
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "no such file or directory") ||
		strings.Contains(errStr, "transport is closing") ||
		strings.Contains(errStr, "clientconn is closing") ||
		strings.Contains(errStr, "error reading server preface") ||
		strings.Contains(errStr, "connection closed") ||
		strings.Contains(errStr, "unavailable")
}

func withReconnect[T any](c *reconnectingClient, call func(Client) (T, error)) (T, error) {
	var zero T
	client, err := c.ensureClient()
	if err != nil {
		return zero, err
	}
	resp, err := call(client)
	if !shouldReconnect(err) {
		return resp, err
	}
	c.reset()
	client, rebuildErr := c.ensureClient()
	if rebuildErr != nil {
		return zero, err
	}
	return call(client)
}

func (c *reconnectingClient) EnsureNetwork(ctx context.Context, req *EnsureNetworkRequest) (*EnsureNetworkResponse, error) {
	return withReconnect(c, func(client Client) (*EnsureNetworkResponse, error) {
		return client.EnsureNetwork(ctx, req)
	})
}

func (c *reconnectingClient) ReleaseNetwork(ctx context.Context, req *ReleaseNetworkRequest) error {
	_, err := withReconnect(c, func(client Client) (struct{}, error) {
		return struct{}{}, client.ReleaseNetwork(ctx, req)
	})
	return err
}

func (c *reconnectingClient) ReconcileNetwork(ctx context.Context, req *ReconcileNetworkRequest) (*ReconcileNetworkResponse, error) {
	return withReconnect(c, func(client Client) (*ReconcileNetworkResponse, error) {
		return client.ReconcileNetwork(ctx, req)
	})
}

func (c *reconnectingClient) GetNetwork(ctx context.Context, req *GetNetworkRequest) (*GetNetworkResponse, error) {
	return withReconnect(c, func(client Client) (*GetNetworkResponse, error) {
		return client.GetNetwork(ctx, req)
	})
}

func (c *reconnectingClient) ListNetworks(ctx context.Context, req *ListNetworksRequest) (*ListNetworksResponse, error) {
	return withReconnect(c, func(client Client) (*ListNetworksResponse, error) {
		return client.ListNetworks(ctx, req)
	})
}

func (c *reconnectingClient) Health(ctx context.Context, req *HealthRequest) error {
	_, err := withReconnect(c, func(client Client) (struct{}, error) {
		return struct{}{}, client.Health(ctx, req)
	})
	return err
}

func httpClientEnabled() bool {
	return strings.TrimSpace(os.Getenv(enableHTTPClientEnv)) == "1"
}

func NewNoopClient() Client {
	return &noopClient{}
}

func (c *noopClient) EnsureNetwork(_ context.Context, _ *EnsureNetworkRequest) (*EnsureNetworkResponse, error) {
	return nil, ErrNotConfigured
}

func (c *noopClient) ReleaseNetwork(_ context.Context, _ *ReleaseNetworkRequest) error {
	return ErrNotConfigured
}

func (c *noopClient) ReconcileNetwork(_ context.Context, _ *ReconcileNetworkRequest) (*ReconcileNetworkResponse, error) {
	return nil, ErrNotConfigured
}

func (c *noopClient) GetNetwork(_ context.Context, _ *GetNetworkRequest) (*GetNetworkResponse, error) {
	return nil, ErrNotConfigured
}

func (c *noopClient) ListNetworks(_ context.Context, _ *ListNetworksRequest) (*ListNetworksResponse, error) {
	return nil, ErrNotConfigured
}

func (c *noopClient) Health(_ context.Context, _ *HealthRequest) error {
	return ErrNotConfigured
}
