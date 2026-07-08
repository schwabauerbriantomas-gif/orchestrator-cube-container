// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package networkagentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type httpClient struct {
	baseURL string
	client  *http.Client
}

func newHTTPClient(baseURL string) Client {
	return newHTTPClientWithClient(baseURL, &http.Client{
		Timeout: 5 * time.Second,
	})
}

func newHTTPClientWithClient(baseURL string, hc *http.Client) Client {
	return &httpClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  hc,
	}
}

func newUnixHTTPClient(endpoint string) (Client, error) {
	socketPath := strings.TrimPrefix(endpoint, "unix://")
	if strings.TrimSpace(socketPath) == "" {
		return nil, ErrInvalidEndpoint
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 3 * time.Second,
		}).DialContext,
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
	}
	hc := &http.Client{
		Timeout:   5 * time.Second,
		Transport: transport,
	}
	return newHTTPClientWithClient("http://unix", hc), nil
}

func (c *httpClient) EnsureNetwork(ctx context.Context, req *EnsureNetworkRequest) (*EnsureNetworkResponse, error) {
	resp := &EnsureNetworkResponse{}
	if err := c.postJSON(ctx, "/v1/network/ensure", req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *httpClient) ReleaseNetwork(ctx context.Context, req *ReleaseNetworkRequest) error {
	resp := &ReleaseNetworkResponse{}
	return c.postJSON(ctx, "/v1/network/release", req, resp)
}

func (c *httpClient) ReconcileNetwork(ctx context.Context, req *ReconcileNetworkRequest) (*ReconcileNetworkResponse, error) {
	resp := &ReconcileNetworkResponse{}
	if err := c.postJSON(ctx, "/v1/network/reconcile", req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *httpClient) GetNetwork(ctx context.Context, req *GetNetworkRequest) (*GetNetworkResponse, error) {
	resp := &GetNetworkResponse{}
	if err := c.postJSON(ctx, "/v1/network/get", req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *httpClient) ListNetworks(ctx context.Context, req *ListNetworksRequest) (*ListNetworksResponse, error) {
	if req == nil {
		req = &ListNetworksRequest{}
	}
	resp := &ListNetworksResponse{}
	if err := c.postJSON(ctx, "/v1/network/list", req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *httpClient) Health(ctx context.Context, req *HealthRequest) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("healthz status=%d body=%s", httpResp.StatusCode, string(data))
	}
	return nil
}

func (c *httpClient) postJSON(ctx context.Context, path string, reqBody interface{}, out interface{}) error {
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(httpResp.Body)
		return fmt.Errorf("request %s failed status=%d body=%s", path, httpResp.StatusCode, string(data))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(httpResp.Body).Decode(out)
}
