// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package ws

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// TestMonitoredConnBytes tests the bytes read and written by a monitored connection.
// This is a matrix test: one dimension is the method used to send messages (either WriteMessage or NextWriter) and
// the other dimension is the content of the messages sent.
func TestMonitoredConnBytes(t *testing.T) { //nolint:gocognit,funlen
	t.Parallel()

	tests := []struct {
		name          string
		messages      []string
		expectedBytes int64
	}{
		{
			name:          "single message",
			messages:      []string{"hello"},
			expectedBytes: 5,
		},
		{
			name:          "empty message",
			messages:      []string{""},
			expectedBytes: 0,
		},
		{
			name:          "no messages",
			messages:      []string{},
			expectedBytes: 0,
		},
		{
			name:          "three messages",
			messages:      []string{"hello", "beautiful", "world"},
			expectedBytes: 5 + 9 + 5,
		},
	}

	sendMethods := []struct {
		name string
		send func(*monitoredConn, string) error
	}{
		{
			name: "WriteMessage",
			send: func(conn *monitoredConn, message string) error {
				return conn.WriteMessage(websocket.TextMessage, []byte(message))
			},
		},
		{
			name: "NextWriter",
			send: func(conn *monitoredConn, message string) error {
				writer, err := conn.NextWriter(websocket.TextMessage)
				if err != nil {
					return err
				}
				defer writer.Close()
				_, err = writer.Write([]byte(message))
				return err //nolint:wrapcheck
			},
		},
	}

	for _, tt := range tests {
		for _, method := range sendMethods {
			t.Run(fmt.Sprintf("%s - %s", method.name, tt.name), func(t *testing.T) {
				t.Parallel()

				server := mustStartEchoServer(t)
				defer server.Close()

				conn := mustDialServer(t, server)
				defer conn.Close()

				go func() {
					for _, message := range tt.messages {
						if err := conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
							t.Errorf("Failed to write message: %v", err)
						}
					}
					err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					if err != nil {
						t.Errorf("Failed to write close message: %v", err)
					}
				}()

				for {
					_, _, err := conn.ReadMessage()
					if err != nil {
						if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
							// Normal closure, exit the loop.
							break
						}
						t.Fatalf("Failed to read message: %v", err)
					}
				}

				if conn.Stats().BytesWritten != tt.expectedBytes {
					t.Errorf("Expected bytesWritten %d, got %d", tt.expectedBytes, conn.Stats().BytesWritten)
				}

				if conn.Stats().BytesRead != tt.expectedBytes {
					t.Errorf("Expected bytesRead %d, got %d", tt.expectedBytes, conn.Stats().BytesRead)
				}
			})
		}
	}
}

func mustDialServer(t *testing.T, server *httptest.Server) *monitoredConn {
	t.Helper()
	wsURL := strings.Replace(server.URL, "http", "ws", 1)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	return NewMonitoredConn(conn, "test-connection", "test-direction", slog.New(slog.NewTextHandler(os.Stdout, nil)))
}

func mustStartEchoServer(t *testing.T) *httptest.Server {
	t.Helper()

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Failed to upgrade connection: %v", err)
			return
		}
		defer conn.Close()

		for {
			messageType, p, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(messageType, p); err != nil {
				return
			}
		}
	})

	return httptest.NewServer(handler)
}
