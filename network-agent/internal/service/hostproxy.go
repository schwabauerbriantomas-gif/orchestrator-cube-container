// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type hostProxy struct {
	listener net.Listener
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

func newHostProxy(bindIP string, hostPort int32, tapName string, guestIP string, guestPort int32, timeoutSeconds int) (*hostProxy, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(bindIP, strconv.Itoa(int(hostPort))))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &hostProxy{
		listener: listener,
		cancel:   cancel,
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.serve(ctx, tapName, guestIP, guestPort, timeoutSeconds)
	}()
	return p, nil
}

func (p *hostProxy) Close() error {
	if p == nil {
		return nil
	}
	p.cancel()
	err := p.listener.Close()
	p.wg.Wait()
	return err
}

func (p *hostProxy) serve(ctx context.Context, tapName string, guestIP string, guestPort int32, timeoutSeconds int) {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handleConn(conn, tapName, guestIP, guestPort, timeoutSeconds)
		}()
	}
}

func (p *hostProxy) handleConn(clientConn net.Conn, tapName string, guestIP string, guestPort int32, timeoutSeconds int) {
	defer clientConn.Close()
	dialer := &net.Dialer{
		Timeout: timeDurationSeconds(timeoutSeconds),
		Control: func(network, address string, c syscall.RawConn) error {
			var ctrlErr error
			if err := c.Control(func(fd uintptr) {
				ctrlErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, tapName)
			}); err != nil {
				return err
			}
			return ctrlErr
		},
	}
	backendConn, err := dialer.Dial("tcp", net.JoinHostPort(guestIP, strconv.Itoa(int(guestPort))))
	if err != nil {
		return
	}
	defer backendConn.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(backendConn, clientConn)
		if c, ok := backendConn.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, backendConn)
		if c, ok := clientConn.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
	}()
	wg.Wait()
}

func firstGuestIP(interfaces []Interface) (string, error) {
	if len(interfaces) == 0 || len(interfaces[0].IPs) == 0 {
		return "", fmt.Errorf("guest ip is empty")
	}
	ip, _, err := net.ParseCIDR(interfaces[0].IPs[0])
	if err != nil {
		return "", err
	}
	return ip.String(), nil
}

func timeDurationSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}
