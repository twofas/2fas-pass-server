// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package wstunnel

import (
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/twofas/2fas-pass-server/server/connection"
	"github.com/twofas/2fas-pass-server/server/ws"
)

// clientStream is a bidirectional stream between client and server.
// Two streams make a Tunnel.
type clientStream struct {
	conn        ws.Conn
	receiver    serverSocket
	config      Config
	done        <-chan struct{}
	close       signalClose
	closeReason *atomic.Pointer[closeReason]
	tunnelState *tunnelState
	clientOrder clientOrder
}

func (c *clientStream) start() error {
	if c.clientOrder == clientConnectedFirst {
		go c.keepDrainingMessagesUntilAnotherClientConnects()
	} else {
		select {
		case <-c.tunnelState.firstClientStoppedDraining:
		case <-c.done:
			return nil
		}
	}

	// This is only to handle the errors. In happy path, the connection is closed by the client.
	defer c.close(websocket.CloseInternalServerErr, "abnormal closure")

	g := errgroup.Group{}
	g.Go(c.readPumpWithClose)
	g.Go(c.writePumpWithClose)

	if err := g.Wait(); err != nil {
		return fmt.Errorf("clientStream between client and server failed: %w", err)
	}
	return nil
}

func (c *clientStream) keepDrainingMessagesUntilAnotherClientConnects() {
	ok := true
	for ok {
		select {
		case <-c.done:
			ok = false
		case <-c.tunnelState.allClientsConnected:
			ok = false
		case <-c.receiver.write:
			ok = true
		}
	}
	c.tunnelState.onFirstClientStoppedDraining()
}

func (c *clientStream) readPumpWithClose() error {
	c.conn.SetReadLimit(c.config.MaxMessageSize)
	if err := c.setReadDeadline(); err != nil {
		return err
	}
	c.conn.SetPongHandler(func(string) error {
		if err := c.setReadDeadline(); err != nil {
			return fmt.Errorf("failed to set read deadline: %w", err)
		}
		return nil
	})
	err := c.readPump()

	// close will be noop if it has been called before.
	if err == nil {
		c.close(connection.CloseAnotherPartyDisconnectedNormally, "another party went away")
	} else {
		c.close(connection.CloseAnotherPartyDisconnectedAbnormally, "abnormal closure from read")
	}
	if err != nil {
		return fmt.Errorf("readPumpWithClose failed: %w", err)
	}
	return nil
}

func (c *clientStream) setReadDeadline() error {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.config.ReadTimeout)); err != nil {
		return fmt.Errorf("failed to set read deadline: %w", err)
	}
	return nil
}

// readPump pumps messages from the websocket to the reader chan.
func (c *clientStream) readPump() error {
	for {
		message, ok, err := c.readOneMessage()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		select {
		case c.receiver.write <- message:
		case <-c.done:
			return nil
		}
	}
}

// readOneMessage from the websocket. It returns: the message, if the connection is still open and error if any.
func (c *clientStream) readOneMessage() ([]byte, bool, error) {
	expectedClosureReasons := []int{
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
		websocket.CloseNormalClosure,
	}
	if err := c.setReadDeadline(); err != nil {
		return nil, false, err
	}
	_, message, err := c.conn.ReadMessage()
	if err != nil {
		if websocket.IsCloseError(err, expectedClosureReasons...) {
			// it is expected close error, return nil
			return nil, false, nil
		}
		if websocket.IsUnexpectedCloseError(err, expectedClosureReasons...) {
			return nil, false, fmt.Errorf("websocket closed unexpected: %w", err)
		}
		if errors.Is(err, net.ErrClosed) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("failed to read message: %w", err)
	}
	return message, true, nil
}

func (c *clientStream) writePumpWithClose() error {
	err := c.writePump()
	// close will be noop if it has been called before.
	if err == nil {
		c.close(connection.CloseAnotherPartyDisconnectedNormally, "normal closure")
	} else {
		c.close(connection.CloseAnotherPartyDisconnectedAbnormally, "abnormal closure from write")
	}
	// We have just called close, so we are sure that the reason is set.
	reason := c.closeReason.Load()

	// At this point it's best effort to send the close message, so we ignore the errors.
	_ = c.conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout))
	_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(reason.status, reason.reason))
	_ = c.conn.Close()
	if err != nil {
		return fmt.Errorf("writePumpWithClose failed: %w", err)
	}
	return nil
}

// writePump pumps messages from the reader chan to the websocket Tunnel.
//
// A goroutine running writePump is started for each Tunnel.
func (c *clientStream) writePump() error {
	ticker := time.NewTicker(c.config.PingFrequency)
	defer ticker.Stop()

	for {
		select {
		case message, ok := <-c.receiver.read:
			if !ok {
				return nil
			}
			if err := c.conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
				return fmt.Errorf("failed to set write deadline: %w", err)
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return fmt.Errorf("failed to get next writer: %w", err)
			}
			_, err = w.Write(message)
			if err != nil {
				return fmt.Errorf("failed to write message: %w", err)
			}

			if err := w.Close(); err != nil {
				return fmt.Errorf("failed to close writer: %w", err)
			}
		case <-ticker.C:
			if err := c.conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
				return fmt.Errorf("failed to set write deadline: %w", err)
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return fmt.Errorf("failed to write ping message: %w", err)
			}
		case <-c.done:
			return nil
		}
	}
}
