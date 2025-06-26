// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package wstunnel

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/twofas/2fas-pass-server/server/ws"
)

var ErrAlreadyConnected = errors.New("already connected")

// Config is configuration for the Tunnel.
type Config struct {
	MaxMessageSize int64         `env:"WS_MAX_MESSAGE_SIZE" env-default:"3145728"`
	ReadTimeout    time.Duration `env:"WS_READ_TIMEOUT" env-default:"10s"`
	WriteTimeout   time.Duration `env:"WS_WRITE_TIMEOUT" env-default:"10s"`
	PingFrequency  time.Duration `env:"WS_PING_FREQUENCY" env-default:"2s"`
}

type closeReason struct {
	status int
	reason string
}

// Tunnel is a bidirectional stream between two clients (left and right).
// Tunnel has the following lifecycle:
//  1. First client connects to the tunnel. If this clients sends a message, it will be discarded,
//  2. Second client connects to the tunnel.
//     Now the first client stops "draining" phase and both clients go into active phase.
type Tunnel struct {
	leftToRight chan []byte
	rightToLeft chan []byte
	config      Config
	tunnelState *tunnelState

	// cancel and done are obtained using context.WithCancel.
	// This wait cancel can be safely called multiple times and/ord by multiple goroutines.
	// (as opposed to close(done) which could be only be called once).
	cancel      func()
	done        <-chan struct{}
	closeReason *atomic.Pointer[closeReason]
}

// New creates a new Tunnel.
func New(config Config) *Tunnel {
	ctx, cancel := context.WithCancel(context.Background())

	return &Tunnel{
		leftToRight: make(chan []byte),
		rightToLeft: make(chan []byte),
		config:      config,
		done:        ctx.Done(),
		cancel:      cancel,
		closeReason: &atomic.Pointer[closeReason]{},
		tunnelState: newTunnelState(),
	}
}

// ServeLeft handles connection from the left client.
// It is callers responsibility to call this once. It blocks until the connection is closed.
func (t Tunnel) ServeLeft(conn ws.Conn) error {
	clientOrder, err := t.tunnelState.onClientConnected(tunnelDirectionLeft)
	if err != nil {
		return err
	}

	c := &clientStream{
		conn:        conn,
		receiver:    serverSocket{write: t.rightToLeft, read: t.leftToRight},
		config:      t.config,
		done:        t.done,
		close:       t.Close,
		closeReason: t.closeReason,
		tunnelState: t.tunnelState,
		clientOrder: clientOrder,
	}

	return c.start()
}

// ServeRight handles connection from the right client.
// It is callers responsibility to call this once. It blocks until the connection is closed.
func (t Tunnel) ServeRight(conn ws.Conn) error {
	clientOrder, err := t.tunnelState.onClientConnected(tunnelDirectionRight)
	if err != nil {
		return err
	}

	c := &clientStream{
		conn:        conn,
		receiver:    serverSocket{write: t.leftToRight, read: t.rightToLeft},
		config:      t.config,
		done:        t.done,
		close:       t.Close,
		closeReason: t.closeReason,
		tunnelState: t.tunnelState,
		clientOrder: clientOrder,
	}
	return c.start()
}

// Close the tunnel. It will close all connections. Can be called multiple times and from the multiple goroutines.
func (t Tunnel) Close(status int, reason string) {
	ok := t.closeReason.CompareAndSwap(nil, &closeReason{status: status, reason: reason})
	if !ok {
		return
	}
	t.cancel()
}

type signalClose func(status int, reason string)

// serverSocket is server part of the clientStream.
type serverSocket struct {
	write chan []byte
	read  chan []byte
}
