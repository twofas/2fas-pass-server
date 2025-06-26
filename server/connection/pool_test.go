// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package connection_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/twofas/2fas-pass-server/server/connection"
	"github.com/twofas/2fas-pass-server/server/ws"
)

var dontCloseConnections = connection.Config{
	ConnectionTimeout: 24 * time.Hour,
}

func TestHappyPathWithWebsockets(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, dontCloseConnections)
	id := "id"

	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		handler, err := pool.ServeLeft(id)
		if err != nil {
			t.Errorf("Unexpected error from ServeLeft: %v", err)
			return
		}
		if err := handler(&websocket.Conn{}); err != nil {
			t.Errorf("Unexpected error from ServeLeft handler: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		var handler connection.WebsocketHandler
		for {
			var err error
			handler, err = pool.ServeRight(id)
			if err == nil {
				break
			}
			if errors.Is(err, connection.ErrLeftNotConnected) {
				t.Logf("Left is not connected yet, retrying")
				continue
			}
			t.Errorf("Unexpected error from ServeRight: %v", err)
			return
		}
		if err := handler(&websocket.Conn{}); err != nil {
			t.Errorf("Unexpected error from ServeRight handler: %v", err)
		}
	}()

	tunnel := waitForTunnelToBeUsed(t, tf)
	tunnel.Close(0, "")

	wg.Wait()
}

func waitForTunnelToBeUsed(t *testing.T, tf *tunnelFactory) *mockTunnel {
	t.Helper()

	for {
		tunnels := tf.getTunnels()
		if len(tunnels) > 1 {
			t.Fatalf("Unexpected number of tunnels: %d", len(tunnels))
		}
		if len(tunnels) == 0 {
			continue
		}
		tunnel := tunnels[0]
		t.Logf("Left: %d, Right: %d, Close: %d",
			tunnel.leftCalled.Load(),
			tunnel.rightCalled.Load(),
			tunnel.closeCalled.Load())

		if tunnel.leftCalled.Load() == 1 && tunnel.rightCalled.Load() == 1 {
			return tunnel
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHappyPath(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, dontCloseConnections)
	id := "id"

	_, err := pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_, err = pool.ServeRight(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestLeftCanBeCalledOnlyOnce(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, dontCloseConnections)
	id := "id"

	_, err := pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_, err = pool.ServeLeft(id)
	if !errors.Is(err, connection.ErrAlreadyConnected) {
		t.Fatalf("expected ErrAlreadyConnected, got %v", err)
	}

	tunnels := tf.getTunnels()
	if len(tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(tf.tunnels))
	}
	for _, tunnel := range tunnels {
		// We expect no calls to ServeLeft, because we never use the handler returned by pool.ServeLeft.
		assertNumberOfTunnelCalls(t, tunnel, 0, 0, 1)
	}
}

func TestRightCanBeCalledOnlyOnce(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, dontCloseConnections)
	id := "id"

	_, err := pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_, err = pool.ServeRight(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_, err = pool.ServeRight(id)
	if !errors.Is(err, connection.ErrAlreadyConnected) {
		t.Fatalf("expected ErrAlreadyConnected, got %v", err)
	}

	tunnels := tf.getTunnels()
	if len(tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(tf.tunnels))
	}
	for _, tunnel := range tunnels {
		// We expect no calls to ServeLeft or ServerRight, because we never use the handler returned by pool.
		assertNumberOfTunnelCalls(t, tunnel, 0, 0, 1)
	}
}

func TestAnotherLeftWillCloseEveryone(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, dontCloseConnections)
	id := "id"

	_, err := pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_, err = pool.ServeRight(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	_, err = pool.ServeLeft(id)
	if !errors.Is(err, connection.ErrAlreadyConnected) {
		t.Fatalf("expected ErrAlreadyConnected, got %v", err)
	}

	tunnels := tf.getTunnels()
	if len(tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(tf.tunnels))
	}
	for _, tunnel := range tunnels {
		// We expect no calls to ServeLeft or ServerRight, because we never use the handler returned by pool.
		assertNumberOfTunnelCalls(t, tunnel, 0, 0, 1)
	}
}

func TestConnectionsAreClosedAfterTimeout(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, connection.Config{
		ConnectionTimeout: 50 * time.Millisecond,
	})
	id := "id"

	wg := sync.WaitGroup{}
	wg.Add(1)

	h, err := pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	go func() {
		defer wg.Done()
		err := h(&websocket.Conn{})
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	wg.Wait()

	tunnels := tf.getTunnels()
	if len(tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(tf.tunnels))
	}
	for _, tunnel := range tunnels {
		assertNumberOfTunnelCalls(t, tunnel, 1, 0, 1)
	}
}

func TestIDsCanBeReusedAfterTimeout(t *testing.T) {
	tf := newTunnelFactory()
	pool := connection.NewPool(tf.newMockTunnel, connection.Config{
		ConnectionTimeout: 50 * time.Millisecond,
	})
	id := "id"

	_, err := pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	_, err = pool.ServeLeft(id)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	tunnels := tf.getTunnels()
	if len(tunnels) != 2 {
		t.Fatalf("expected two tunnels, got %d", len(tf.tunnels))
	}
}

func assertNumberOfTunnelCalls(t *testing.T, tunnel *mockTunnel, left, right, closeCount int32) { //nolint:unparam
	t.Helper()
	ok := tunnel.leftCalled.Load() == left &&
		tunnel.rightCalled.Load() == right &&
		tunnel.closeCalled.Load() == closeCount
	if !ok {
		t.Fatalf("expected %d calls to ServeLeft, %d calls to ServeRight and %d calls to Close, got %d, %d, %d",
			left,
			right,
			closeCount,
			tunnel.leftCalled.Load(),
			tunnel.rightCalled.Load(),
			tunnel.closeCalled.Load())
	}
}

type mockTunnel struct {
	leftCalled, rightCalled, closeCalled *atomic.Int32
	waitLeft, waitRight                  chan struct{}
}

func (m mockTunnel) Close(_ int, _ string) {
	if m.closeCalled.Add(1) == 1 {
		close(m.waitLeft)
		close(m.waitRight)
	}
}

func (m mockTunnel) ServeLeft(_ ws.Conn) error {
	m.leftCalled.Add(1)
	<-m.waitLeft
	return nil
}

func (m mockTunnel) ServeRight(_ ws.Conn) error {
	m.rightCalled.Add(1)
	<-m.waitRight
	return nil
}

func newMockTunnel() *mockTunnel {
	return &mockTunnel{
		leftCalled:  &atomic.Int32{},
		rightCalled: &atomic.Int32{},
		closeCalled: &atomic.Int32{},
		waitLeft:    make(chan struct{}),
		waitRight:   make(chan struct{}),
	}
}

func newTunnelFactory() *tunnelFactory {
	return &tunnelFactory{
		mtx: &sync.Mutex{},
	}
}

type tunnelFactory struct {
	mtx     *sync.Mutex
	tunnels []*mockTunnel
}

func (tf *tunnelFactory) getTunnels() []*mockTunnel {
	tf.mtx.Lock()
	defer tf.mtx.Unlock()
	return tf.tunnels
}

func (tf *tunnelFactory) newMockTunnel() connection.Tunnel {
	tf.mtx.Lock()
	defer tf.mtx.Unlock()
	t := newMockTunnel()
	tf.tunnels = append(tf.tunnels, t)
	return t
}
