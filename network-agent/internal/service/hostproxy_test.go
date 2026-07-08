// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package service

import (
	"io"
	"net"
	"testing"
)

func TestFirstGuestIP(t *testing.T) {
	ip, err := firstGuestIP([]Interface{{
		IPs: []string{"169.254.68.6/30", "10.0.0.2/24"},
	}})
	if err != nil {
		t.Fatalf("firstGuestIP error=%v", err)
	}
	if ip != "169.254.68.6" {
		t.Fatalf("firstGuestIP=%q, want=%q", ip, "169.254.68.6")
	}
}

func TestHostProxyLoopback(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen error=%v", err)
	}
	defer backend.Close()
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("pong"))
	}()

	proxy, err := newHostProxy("127.0.0.1", 0, "lo", "127.0.0.1", int32(backend.Addr().(*net.TCPAddr).Port), 2)
	if err != nil {
		t.Fatalf("newHostProxy error=%v", err)
	}
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial proxy error=%v", err)
	}
	defer conn.Close()
	data, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read proxied data error=%v", err)
	}
	if string(data) != "pong" {
		t.Fatalf("proxied payload=%q, want=%q", string(data), "pong")
	}
}
