// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package wstunnel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/twofas/2fas-pass-server/server/connection"
	"github.com/twofas/2fas-pass-server/server/ws"
)

const (
	DefaultWriteTimeout  = 30 * time.Second
	DefaultReadTimeout   = 30 * time.Second
	DefaultPingFrequency = DefaultReadTimeout / 10
)

func DefaultConfig() Config {
	return Config{
		WriteTimeout:  DefaultWriteTimeout,
		ReadTimeout:   DefaultReadTimeout,
		PingFrequency: DefaultPingFrequency,
	}
}

// makeWsURL constructs the WebSocket from the test server's URL.
func makeWsURL(s, app string) string {
	return fmt.Sprintf("ws%s/%s", strings.TrimPrefix(s, "http"), app)
}

var testDialer = websocket.Dialer{
	Subprotocols:     []string{"test"},
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	HandshakeTimeout: 30 * time.Second,
}

// TestHappyPath starts server, connects two clients and tests that messages are proxied both ways.
func TestHappyPath(t *testing.T) {
	tunnel := New(DefaultConfig())

	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	rightConn := mustWSDial(t, addr, "right")
	defer leftConn.Close()
	defer rightConn.Close()

	testProxyBothWays(t, leftConn, rightConn, "left->right", "right->left")
}

// TestClosingTunnel tests that closing the tunnel closes the connection.
func TestClosingTunnel(t *testing.T) {
	tunnel := New(DefaultConfig())
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	defer leftConn.Close()

	tunnel.Close(websocket.CloseInternalServerErr, "test")

	assertConnectionWasClosed(t, leftConn, websocket.CloseInternalServerErr)
}

// TestClosingTunnelWithTwoConnections tests that closing the tunnel closes both connections.
func TestClosingTunnelWithTwoConnections(t *testing.T) {
	tunnel := New(DefaultConfig())
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	rightConn := mustWSDial(t, addr, "right")
	defer leftConn.Close()
	defer rightConn.Close()

	tunnel.Close(websocket.CloseInternalServerErr, "test")

	assertConnectionWasClosed(t, leftConn, websocket.CloseInternalServerErr)
	assertConnectionWasClosed(t, rightConn, websocket.CloseInternalServerErr)
}

// TestClosingActiveConnection tests that closing the tunnel while messages are being sent.
func TestClosingActiveConnection(t *testing.T) {
	tunnel := New(DefaultConfig())
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	rightConn := mustWSDial(t, addr, "right")
	defer leftConn.Close()
	defer rightConn.Close()

	var leftConnReadErr, leftConnWriteErr, rightConnReadErr, rightConnWriteErr error
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		leftConnWriteErr, rightConnReadErr = keepSendingMessages(leftConn, rightConn, "left->right")
	}()
	go func() {
		defer wg.Done()
		rightConnWriteErr, leftConnReadErr = keepSendingMessages(rightConn, leftConn, "right->left")
	}()
	tunnel.Close(websocket.CloseAbnormalClosure, "test")
	wg.Wait()

	if leftConnReadErr == nil || leftConnWriteErr == nil || rightConnReadErr == nil || rightConnWriteErr == nil {
		t.Fatalf("Expected errors, got %v, %v, %v, %v",
			leftConnReadErr,
			leftConnWriteErr,
			rightConnReadErr,
			rightConnWriteErr)
	}

	// TODO: Right now connection are (sometimes) closed with wrong reason (broken pipe). Once this is fixed,
	// we should assert the close reason.
}

// TestTunnelHandlesLargeMessages tests that the tunnel can handle messages according to the max message size.
func TestTunnelHandlesLargeMessages(t *testing.T) {
	tunnel := New(Config{
		MaxMessageSize: 1024 * 1024, // 1MB
		ReadTimeout:    DefaultReadTimeout,
		WriteTimeout:   DefaultWriteTimeout,
		PingFrequency:  DefaultPingFrequency,
	})
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	rightConn := mustWSDial(t, addr, "right")
	defer leftConn.Close()
	defer rightConn.Close()

	largeMessage := strings.Repeat("a", 1024*1024) // 1MB message

	testProxyBothWays(t, leftConn, rightConn, largeMessage, largeMessage)
}

// TestTunnelMessagesWithNewline tests that the tunnel can handle messages with newline inside.
func TestTunnelMessagesWithNewline(t *testing.T) {
	tunnel := New(Config{
		MaxMessageSize: 1024 * 1024, // 1MB
		ReadTimeout:    DefaultReadTimeout,
		WriteTimeout:   DefaultWriteTimeout,
		PingFrequency:  DefaultPingFrequency,
	})
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	rightConn := mustWSDial(t, addr, "right")
	defer leftConn.Close()
	defer rightConn.Close()

	largeMessage := strings.Repeat("message\n", 1024)

	testProxyBothWays(t, leftConn, rightConn, largeMessage, largeMessage)
}

// TestTunnelHandlesLargeMessages tests that the tunnel will close the connection if the message is too big.
func TestTunnelTooBigMessage(t *testing.T) {
	tunnel := New(Config{
		MaxMessageSize: 1024 * 1024, // 1MB
		ReadTimeout:    DefaultReadTimeout,
		WriteTimeout:   DefaultWriteTimeout,
		PingFrequency:  DefaultPingFrequency,
	})
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	t.Logf("Started test server at %s", addr)

	leftConn := mustWSDial(t, addr, "left")
	defer leftConn.Close()

	err := leftConn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("a", 1024*1024+1)))
	if err != nil {
		// Write succeeds, but the next operation will fail.
		t.Fatalf("Failed to write message: %v", err)
	}
	_, _, err = leftConn.ReadMessage()
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("Expected CloseMessageTooBig, got %v", err)
	}
}

// TestTunnelUsesPingForKeepAlive tests that idle connections are kept alive with ping messages.
// For this test, we use a short read timeout and a high ping frequency.
//
// Test starts two connections, waits and then sends a message from right to left and from left to right.
// Connections should be still alive, thanks to the ping messages.
func TestTunnelUsesPingForKeepAlive(t *testing.T) {
	readWriteTimeout := 100 * time.Millisecond

	tunnel := New(Config{
		MaxMessageSize: 1024, // 1KB
		ReadTimeout:    readWriteTimeout,
		WriteTimeout:   readWriteTimeout,
		PingFrequency:  readWriteTimeout / 4,
	})
	addr, closeServer := startTestServer(t, tunnel)
	defer closeServer()

	// We will be writing from multiple goroutines, and we need to ensure that the writes are synchronized.
	syncedWrite := func(mtx *sync.Mutex, conn *websocket.Conn, msgType int, message string) error {
		mtx.Lock()
		defer mtx.Unlock()
		return conn.WriteMessage(msgType, []byte(message))
	}

	leftConnMtx := &sync.Mutex{}
	leftConn := mustWSDial(t, addr, "left")
	leftConn.SetPingHandler(func(appData string) error {
		return syncedWrite(leftConnMtx, leftConn, websocket.PongMessage, appData)
	})
	defer leftConn.Close()

	rightConnMtx := &sync.Mutex{}
	rightConn := mustWSDial(t, addr, "right")
	rightConn.SetPingHandler(func(appData string) error {
		return syncedWrite(rightConnMtx, rightConn, websocket.PongMessage, appData)
	})
	defer rightConn.Close()

	wx := errgroup.Group{}
	wx.Go(func() error {
		_, _, err := leftConn.ReadMessage()
		if err != nil {
			return fmt.Errorf("failed to read message: %w", err)
		}
		return nil
	})
	wx.Go(func() error {
		_, _, err := rightConn.ReadMessage()
		if err != nil {
			return fmt.Errorf("failed to read message: %w", err)
		}
		return nil
	})

	time.Sleep(3 * readWriteTimeout)

	if err := syncedWrite(leftConnMtx, leftConn, websocket.BinaryMessage, "right->left"); err != nil {
		t.Fatal(err)
	}
	if err := syncedWrite(rightConnMtx, rightConn, websocket.BinaryMessage, "right->left"); err != nil {
		t.Fatal(err)
	}

	if err := wx.Wait(); err != nil {
		t.Fatal(err)
	}
}

// TestTunnelCanBeClosedTwice tests that closing the tunnel twice does not panic.
func TestTunnelCanBeClosedTwice(_ *testing.T) {
	tunnel := New(DefaultConfig())

	tunnel.Close(websocket.CloseAbnormalClosure, "test")
	tunnel.Close(websocket.CloseAbnormalClosure, "test")
}

func TestIgnoreMessagesSentBeforeBothPartiesAreConnected(t *testing.T) {
	t.Run("Left, then right", func(t *testing.T) {
		tunnel := New(DefaultConfig())

		addr, closeServer := startTestServer(t, tunnel)
		defer closeServer()

		leftConn := mustWSDial(t, addr, "left")
		defer leftConn.Close()

		for i := 0; i < 50; i++ {
			err := leftConn.WriteMessage(websocket.TextMessage, []byte("left->/dev/null"))
			if err != nil {
				t.Fatalf("Failed to write message: %v", err)
			}
		}

		// Prevent rare race condition where the second is established while the message is being processed.
		time.Sleep(10 * time.Millisecond)

		rightConn := mustWSDial(t, addr, "right")
		defer rightConn.Close()
		testProxyBothWays(t, leftConn, rightConn, "left->right", "right->left")
	})

	t.Run("Right, then left", func(t *testing.T) {
		tunnel := New(DefaultConfig())

		addr, closeServer := startTestServer(t, tunnel)
		defer closeServer()

		rightConn := mustWSDial(t, addr, "right")
		defer rightConn.Close()

		for i := 0; i < 50; i++ {
			err := rightConn.WriteMessage(websocket.TextMessage, []byte("right->/dev/null"))
			if err != nil {
				t.Fatalf("Failed to write message: %v", err)
			}
		}

		// Prevent rare race condition where the second is established while the message is being processed.
		time.Sleep(10 * time.Millisecond)

		leftConn := mustWSDial(t, addr, "left")
		defer leftConn.Close()
		testProxyBothWays(t, leftConn, rightConn, "left->right", "right->left")
	})
}

//nolint:funlen // This is a table tests, params use a lot of LOC
func TestClosingClientConnectionPropagatesWellToAnotherClient(t *testing.T) {
	chooseLeft := func(left, right *websocket.Conn) *websocket.Conn { //nolint:revive
		return left
	}
	chooseRight := func(left, right *websocket.Conn) *websocket.Conn { //nolint:revive
		return right
	}

	tests := []struct {
		name                             string
		connectionToClose                func(left, right *websocket.Conn) *websocket.Conn
		connectionToCheck                func(left, right *websocket.Conn) *websocket.Conn
		closeCode                        int
		expectedCloseCodeInAnotherClient int
	}{
		{
			name:                             "Normal closure from left client is propagated to right client",
			connectionToClose:                chooseLeft,
			connectionToCheck:                chooseRight,
			closeCode:                        websocket.CloseNormalClosure,
			expectedCloseCodeInAnotherClient: connection.CloseAnotherPartyDisconnectedNormally,
		},
		{
			name:                             "Normal closure from right client is propagated to left client",
			connectionToClose:                chooseRight,
			connectionToCheck:                chooseLeft,
			closeCode:                        websocket.CloseNormalClosure,
			expectedCloseCodeInAnotherClient: connection.CloseAnotherPartyDisconnectedNormally,
		},
		{
			name:                             "non-standard closure from left client is propagated to right client",
			connectionToClose:                chooseLeft,
			connectionToCheck:                chooseRight,
			closeCode:                        websocket.ClosePolicyViolation,
			expectedCloseCodeInAnotherClient: connection.CloseAnotherPartyDisconnectedAbnormally,
		},
		{
			name:                             "non-standard closure from right client is propagated to left client",
			connectionToClose:                chooseRight,
			connectionToCheck:                chooseLeft,
			closeCode:                        websocket.ClosePolicyViolation,
			expectedCloseCodeInAnotherClient: connection.CloseAnotherPartyDisconnectedAbnormally,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tunnel := New(DefaultConfig())

			addr, closeServer := startTestServer(t, tunnel)
			defer closeServer()

			leftConn := mustWSDial(t, addr, "left")
			defer leftConn.Close()

			rightConn := mustWSDial(t, addr, "right")
			defer rightConn.Close()

			testProxyBothWays(t, leftConn, rightConn, "left->right", "right->left")

			closeMsg := websocket.FormatCloseMessage(tt.closeCode, "")

			toClose := tt.connectionToClose(leftConn, rightConn)
			if err := toClose.WriteMessage(websocket.CloseMessage, closeMsg); err != nil {
				t.Fatalf("Failed to write close message: %v", err)
			}

			toCheck := tt.connectionToCheck(leftConn, rightConn)
			_, _, err := toCheck.ReadMessage()
			if !websocket.IsCloseError(err, tt.expectedCloseCodeInAnotherClient) {
				t.Fatalf("Expected code %d for closure, got %v", tt.expectedCloseCodeInAnotherClient, err)
			}
		})
	}
}

func assertConnectionWasClosed(t *testing.T, conn *websocket.Conn, status int) {
	t.Helper()

	_, _, err := conn.ReadMessage()
	if websocket.IsCloseError(err, status) {
		t.Logf("Received error %q as expected on timeout", err)
	} else {
		t.Fatalf("Expected abnormal closure, got %v", err)
	}
}

func mustWSDial(t *testing.T, addr, direction string) *websocket.Conn {
	t.Helper()

	url := makeWsURL(addr, direction)
	conn, resp, err := testDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("Failed to dial %q: %v", url, err)
	} else if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("Failed to upgrade clientStream %q, got status %d", url, resp.StatusCode)
	}

	// Prevent rare race condition where the second connectom is established while the message from
	// the first one is being processed.
	time.Sleep(10 * time.Millisecond)
	return conn
}

// testWriteReceive writes a message to writeWS and makes sure it is received by readWS.
func testWriteReceive(t *testing.T, writeWS, readWS *websocket.Conn, message string) {
	t.Helper()
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()

		_, received, err := readWS.ReadMessage()
		if err != nil {
			t.Errorf("Failed to read message: %v", err)
			return
		}
		if string(received) != message {
			t.Errorf("Expected %q, received %q", message, string(received))
		}
	}()

	if err := writeWS.WriteMessage(websocket.BinaryMessage, []byte(message)); err != nil {
		t.Errorf("Failed to write message: %v", err)
	}

	wg.Wait()
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4 * 1024,
	WriteBufferSize: 4 * 1024,
	Subprotocols:    []string{"test"},
}

func startTestServer(t *testing.T, tn *Tunnel) (string, func()) {
	t.Helper()

	requests := &atomic.Int64{}
	mux := http.NewServeMux()
	mux.Handle("/left", handleWS(t, requests, tn.ServeLeft))
	mux.Handle("/right", handleWS(t, requests, tn.ServeRight))

	s := httptest.NewServer(mux)

	return s.URL, func() {
		s.Close()
		tn.Close(websocket.CloseAbnormalClosure, "test is closing")
		for requests.Load() > 0 {
			t.Logf("Waiting for all requests to finish: %d", requests.Load())
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func handleWS(t *testing.T, requests *atomic.Int64, h func(ws ws.Conn) error) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		defer requests.Add(-1)
		conn, err := upgrade(w, req)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to upgrade proxy: %v", err), http.StatusBadRequest)
			return
		}
		if err := h(conn); err != nil {
			t.Logf("Failed to handle connection: %v", err)
		}
	}
}

func upgrade(w http.ResponseWriter, req *http.Request) (*websocket.Conn, error) {
	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to upgrade proxy: %w", err)
	}
	return conn, nil
}

func testProxyBothWays(t *testing.T, leftConn, rightConn *websocket.Conn, leftPayload, rightPayload string) {
	t.Helper()

	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		testWriteReceive(t, leftConn, rightConn, leftPayload)
	}()
	go func() {
		defer wg.Done()
		testWriteReceive(t, rightConn, leftConn, rightPayload)
	}()
	wg.Wait()
}

func keepSendingMessages(writeWS, readWS *websocket.Conn, message string) (error, error) {
	write := errgroup.Group{}
	read := errgroup.Group{}
	read.Go(func() error {
		for {
			_, _, err := readWS.ReadMessage()
			if err != nil {
				return fmt.Errorf("failed read message %q: %w", message, err)
			}
		}
	})
	write.Go(func() error {
		for {
			err := writeWS.WriteMessage(websocket.BinaryMessage, []byte(message))
			if err != nil {
				return fmt.Errorf("failed write message %q: %w", message, err)
			}
			// Flooding the connection can cause tests to fail, especially on CI.
			time.Sleep(10 * time.Millisecond)
		}
	})

	return write.Wait(), read.Wait() //nolint: wrapcheck
}
