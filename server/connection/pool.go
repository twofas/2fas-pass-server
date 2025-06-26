// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package connection

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/twofas/2fas-pass-server/server/ws"
)

var (
	// ErrAlreadyConnected is returned when ServeLeft or ServeRight is called more than once.
	ErrAlreadyConnected = errors.New("already connected")
	// ErrConnectionClosed is returned when ServeLeft or ServeRight is called after Close.
	// Because connections are evicted just after they are closed, this error should not be seen
	// by the clients.
	ErrConnectionClosed = errors.New("connection closed")
	// ErrLeftNotConnected is returned when ServeRight is called before ServeLeft.
	ErrLeftNotConnected = errors.New("left not connected")
)

const (
	CloseExcessClientConnected              = 3001
	CloseConnectionTimeout                  = 3002
	CloseCantCreateProxy                    = 3003
	CloseAnotherPartyDisconnectedNormally   = 3004
	CloseAnotherPartyDisconnectedAbnormally = 3005
	CloseConnectionAlreadyEstablished       = 3006
	CloseBrowserExtensionNotConnected       = 3007
)

// Tunnel is a bidirectional stream between two clients (left and right).
// It is implemented by wsTunnel.Tunnel. We use an interface here to make testing easier.
type Tunnel interface {
	Close(reason int, message string)
	ServeLeft(conn ws.Conn) error
	ServeRight(conn ws.Conn) error
}

// WebsocketHandler handles websocket connections. It is either ServeLeft or ServeRight from the Tunnel.
// It can't be called concurrently. It can be used only once.
type WebsocketHandler func(ws.Conn) error

// connection is a wrapper around Tunnel to ensure that:
// - ServeLeft is called before ServeRight,
// - ServeLeft and ServeRight are called only once.
type connection struct {
	initOnce       *sync.Once
	tunnel         Tunnel
	leftConnected  *atomic.Int32
	rightConnected *atomic.Int32
	closed         *atomic.Bool
	id             string
}

// ensureInit initializes the connection if it hasn't been initialized yet.
func (c *connection) ensureInit(f func() Tunnel) bool {
	ok := false
	c.initOnce.Do(func() {
		ok = true
		c.init(f)
	})
	return ok
}

// init initializes the connection. It must be called by ensureInit, to ensure it is called only once.
func (c *connection) init(f func() Tunnel) {
	c.tunnel = f()
	c.leftConnected = &atomic.Int32{}
	c.rightConnected = &atomic.Int32{}
	c.closed = &atomic.Bool{}
}

func (c *connection) serveLeft() (WebsocketHandler, error) {
	if c.closed.Load() {
		return nil, ErrConnectionClosed
	}
	if !c.leftConnected.CompareAndSwap(0, 1) {
		return nil, ErrAlreadyConnected
	}
	return c.tunnel.ServeLeft, nil
}

func (c *connection) serveRight() (WebsocketHandler, error) {
	if c.closed.Load() {
		return nil, ErrConnectionClosed
	}
	if c.leftConnected.Load() == 0 {
		return nil, ErrLeftNotConnected
	}
	if !c.rightConnected.CompareAndSwap(0, 1) {
		return nil, ErrAlreadyConnected
	}
	return c.tunnel.ServeRight, nil
}

// Close the connection and the underlying tunnel.
func (c *connection) Close(reason int, message string) {
	if c.closed.CompareAndSwap(false, true) {
		c.tunnel.Close(reason, message)
	}
}

// Pool of connections.
// It ensures that each connection is initialized only once, closes them and evicts them after a timeout.
type Pool struct {
	connections   *sync.Map
	tunnelFactory func() Tunnel
	config        Config
}

// Config for the pool.
type Config struct {
	// ConnectionTimeout is the time between the first ServeXXXX call, and the connection is closed.
	ConnectionTimeout time.Duration `env:"CONNECTION_TIMEOUT" env-default:"3m"`
}

// NewPool creates a new connection pool using config and tunnelFactory.
func NewPool(tunnelFactory func() Tunnel, config Config) *Pool {
	return &Pool{
		connections:   &sync.Map{},
		tunnelFactory: tunnelFactory,
		config:        config,
	}
}

// getOrCreate returns the connection with the given id. If it doesn't exist, it creates a new one.
func (p *Pool) getOrCreate(id string) *connection {
	i, _ := p.connections.LoadOrStore(id, &connection{
		initOnce: &sync.Once{},
		id:       id,
	})
	conn := i.(*connection) //nolint:forcetypeassert // We know it's *connection, because we put it in the map.
	if conn.ensureInit(p.tunnelFactory) {
		go p.closeAndDeleteConnectionAfterTimeout(id, conn)
	}

	return conn
}

func (p *Pool) closeAndDeleteConnectionAfterTimeout(id string, conn *connection) {
	time.Sleep(p.config.ConnectionTimeout)
	conn.Close(CloseConnectionTimeout, "timeout")
	p.connections.Delete(id)
}

// get returns the connection with the given id. If it doesn't exist, it creates a new one.
func (p *Pool) get(id string) (*connection, bool) {
	i, ok := p.connections.Load(id)
	if !ok {
		return nil, false
	}
	conn := i.(*connection) //nolint:forcetypeassert // We know it's *connection, because we put it in the map.
	// This is to handle race condition when get and getOrCreate are called at the same time.
	// When this happens, it could happen that get returns a connection before it is initialized.
	if conn.ensureInit(p.tunnelFactory) {
		go p.closeAndDeleteConnectionAfterTimeout(id, conn)
	}
	return conn, true
}

// ServeLeft handles connection from the left client. Error will be returned if ServeLeft has been called before.
func (p *Pool) ServeLeft(id string) (WebsocketHandler, error) {
	proxyConn := p.getOrCreate(id)
	handler, err := proxyConn.serveLeft()
	if err != nil {
		if errors.Is(err, ErrAlreadyConnected) {
			proxyConn.Close(CloseExcessClientConnected, "too many connected clients")
			return nil, err
		}
		proxyConn.Close(CloseCantCreateProxy, "error")
		return nil, err
	}
	return func(conn ws.Conn) error {
		err := handler(conn)

		proxyConn.Close(websocket.CloseNormalClosure, "error")

		// Delete the connection from the pool, so the id can be reused.
		p.connections.Delete(proxyConn.id)

		return err
	}, nil
}

// ServeRight handles connection from the right client. Error will be returned if:
// - ServeLeft has not been called before,
// - ServeRight has been called before,
// - ServeLeft was called more than p.config.ConnectionTimeout ago.
func (p *Pool) ServeRight(id string) (WebsocketHandler, error) {
	proxyConn, ok := p.get(id)
	if !ok {
		return nil, ErrLeftNotConnected
	}
	handler, err := proxyConn.serveRight()
	if err != nil {
		// Race condition. ServeLeft was called on pool, but not on connection (yet).
		if errors.Is(err, ErrLeftNotConnected) {
			return nil, err
		} else if errors.Is(err, ErrAlreadyConnected) {
			proxyConn.Close(CloseExcessClientConnected, "too many connected clients")
			return nil, err
		}
		proxyConn.Close(CloseCantCreateProxy, "error")
		return nil, err
	}
	return func(conn ws.Conn) error {
		err := handler(conn)

		// This is just to make sure that the connection is closed once handler returns.
		// At this point the connection should be closed. If it is, this will have no effect
		// and nothing will be sent to the client.
		proxyConn.Close(websocket.CloseNormalClosure, "error")

		// Delete the connection from the pool, so the id can be reused.
		p.connections.Delete(proxyConn.id)

		return err
	}, nil
}
