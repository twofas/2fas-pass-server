// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avast/retry-go"
	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/twofas/2fas-pass-server/server/connection"
)

func getServerURLWS() string {
	apiURL := os.Getenv("WS_API_URL")
	if apiURL == "" {
		return "ws://localhost:8080"
	}
	return apiURL
}

func getServerURLHTTP() string {
	apiURL := os.Getenv("HTTP_API_URL")
	if apiURL == "" {
		return "http://localhost:8080"
	}
	return apiURL
}

func TestMain(m *testing.M) {
	runE2Estring := os.Getenv("RUN_E2E_TESTS")
	if strings.ToLower(runE2Estring) != "true" {
		//nolint:forbidigo
		fmt.Println("Skipping E2E tests")
		return
	}
	if err := waitForHealthOK(); err != nil {
		//nolint:forbidigo
		fmt.Println("Health check failed: ", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func waitForHealthOK() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return retry.Do(func() error { //nolint:wrapcheck // It's the error we returned from RetryableFunc in retry.Do.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, getServerURLHTTP()+"/health", nil /* body */)
		if err != nil {
			return fmt.Errorf("failed to create health request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to execute health request: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected status: %s", resp.Status)
		}
		return nil
	},
		retry.DelayType(retry.BackOffDelay),
		retry.Context(ctx),
		retry.OnRetry(func(n uint, err error) {
			slog.Info("Failed on healthcheck, retrying",
				slog.Any("error", err),
				slog.Uint64("retry", uint64(n)))
		}))
}

func TestHappyPath(t *testing.T) {
	// First must connect browser extension.
	// Then mobile.
	// Then exchange data.
	connectionID := uuid.NewString()
	beConn := mustDialBrowserExtenstion(t, connectionID)
	defer writeCloseMessageAndClose(t, beConn)

	mobileConn := mustDialMobile(t, connectionID)
	defer writeCloseMessageAndClose(t, mobileConn)

	testProxyBothWays(t, mobileConn, beConn, "mobile->be", "be->mobile")
}

func TestMobileConnectsFirst(t *testing.T) {
	t.Parallel()

	mustDialAndExpectCloseWithError(t, "proxy/mobile/"+uuid.NewString(),
		connection.CloseBrowserExtensionNotConnected,
		"Browser Extension not connected")
}

func TestBrowserTriesConnectTwice(t *testing.T) {
	t.Parallel()

	connectionID := uuid.NewString()
	beConn := mustDialBrowserExtenstion(t, connectionID)

	mustDialAndExpectCloseWithError(t, "proxy/browser_extension/"+connectionID,
		connection.CloseConnectionAlreadyEstablished,
		"Connection for \"browser_extension\" already established")
	_, _, err := beConn.ReadMessage()
	if !websocket.IsCloseError(err, connection.CloseExcessClientConnected) {
		t.Fatalf("Expected normal closure, got %v", err)
	}
}

type notification struct {
	ID   string            `json:"id,omitempty"`
	Data map[string]string `json:"data,omitempty"`
}

type notifications struct {
	Notifications []notification `json:"notifications"`
}

func TestNotifications(t *testing.T) {
	t.Parallel()

	data := map[string]string{
		"key": "value",
	}
	deviceID := uuid.NewString()

	notificationID := mustCreateNotification(t, data, deviceID)

	notifs := mustListNotifications(t, deviceID)
	expectedResult := []notification{
		{
			ID:   notificationID,
			Data: data,
		},
	}
	if diff := cmp.Diff(expectedResult, notifs); diff != "" {
		t.Fatalf("Unexpected data:\n%s", diff)
	}

	mustDeleteNotification(t, deviceID, notificationID)
	notifs = mustListNotifications(t, deviceID)
	if diff := cmp.Diff([]notification{}, notifs); diff != "" {
		t.Fatalf("Unexpected data:\n%s", diff)
	}
}

func mustListNotifications(t *testing.T, deviceID string) []notification {
	t.Helper()

	notificationResponsePayload := notifications{}
	resp, err := http.Get( //nolint:noctx // This code is test.
		fmt.Sprintf("%s/device/%s/notifications", getServerURLHTTP(), deviceID))
	if err != nil {
		t.Fatal("Failed to send request", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&notificationResponsePayload); err != nil {
		t.Fatal("Failed to decode response", err)
	}
	return notificationResponsePayload.Notifications
}

func mustCreateNotification(t *testing.T, data map[string]string, deviceID string) string {
	t.Helper()

	payload := struct {
		Data     map[string]string `json:"data"`
		DeviceID string            `json:"deviceId"`
	}{
		Data:     data,
		DeviceID: deviceID,
	}
	bb, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("Failed to marshal payload", err)
	}

	resp, err := http.Post( //nolint:noctx // This code is test.
		fmt.Sprintf("%s/%s", getServerURLHTTP(), "notifications"),
		"application/json",
		bytes.NewBuffer(bb))
	if err != nil {
		t.Fatal("Failed to send request", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}
	responsePayload := struct {
		NotificationID string `json:"notificationId"`
	}{}
	if err := json.NewDecoder(resp.Body).Decode(&responsePayload); err != nil {
		t.Fatal("Failed to decode response", err)
	}
	return responsePayload.NotificationID
}

func mustDeleteNotification(t *testing.T, deviceID, notificationID string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodDelete, //nolint:noctx // This code is test.
		fmt.Sprintf("%s/device/%s/notifications/%s", getServerURLHTTP(), deviceID, notificationID),
		nil)
	if err != nil {
		t.Fatal("Failed to create request", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("Failed to send request", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestNotificationValidation(t *testing.T) { //nolint:funlen // This is a table test.
	t.Parallel()

	type payload struct {
		Data          map[string]string `json:"data,omitempty"`
		DeviceID      string            `json:"deviceId,omitempty"`
		FcmToken      string            `json:"fcmToken,omitempty"`
		FCMTargetType string            `json:"fcmTargetType"`
	}

	validID := strings.Repeat("a", 256)
	tooLongID := strings.Repeat("a", 257)
	validData := map[string]string{"key": "value"}

	tests := []struct {
		name     string
		payload  payload
		expected int
	}{
		{
			name: "body size too big",
			payload: payload{
				Data: map[string]string{"key": strings.Repeat("a", 262143)},
			},
			expected: http.StatusRequestEntityTooLarge,
		},
		{
			name: "no fcmToken and no notificationId",
			payload: payload{
				Data: validData,
			},
			expected: http.StatusBadRequest,
		},
		{
			name: "fcmToken is too long",
			payload: payload{
				Data:     validData,
				FcmToken: tooLongID,
			},
			expected: http.StatusBadRequest,
		},
		{
			name: "notificationId is too long",
			payload: payload{
				Data:     validData,
				DeviceID: tooLongID,
			},
			expected: http.StatusBadRequest,
		},
		{
			name: "there is no data",
			payload: payload{
				DeviceID: validID,
				FcmToken: validID,
			},
			expected: http.StatusBadRequest,
		},
		{
			name: "invalid target type",
			payload: payload{
				DeviceID:      validID,
				FcmToken:      validID,
				Data:          validData,
				FCMTargetType: "invalid_target_type",
			},
			expected: http.StatusBadRequest,
		},
		{
			name: "correct payload",
			payload: payload{
				DeviceID: validID,
				Data:     validData,
			},
			expected: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bb, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatal("Failed to marshal payload", err)
			}

			resp, err := http.Post( //nolint:noctx // This code is test.
				fmt.Sprintf("%s/%s", getServerURLHTTP(), "notifications"),
				"application/json",
				bytes.NewBuffer(bb))
			if err != nil {
				t.Fatal("Failed to send request", err)
			}
			if resp.StatusCode != tt.expected {
				t.Fatalf("Expected status %d, got %d", tt.expected, resp.StatusCode)
			}
		})
	}
}

func writeCloseMessageAndClose(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	_ = conn.Close()
}

func mustDialBrowserExtenstion(t *testing.T, connectionID string) *websocket.Conn {
	t.Helper()
	return mustWSDial(t, "proxy/browser_extension/"+connectionID)
}

func mustDialMobile(t *testing.T, connectionID string) *websocket.Conn {
	t.Helper()
	return mustWSDial(t, "proxy/mobile/"+connectionID)
}

func mustWSDial(t *testing.T, endpoint string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/%s", getServerURLWS(), endpoint)
	conn, resp, err := testDialer.DialContext(ctx, url, nil)
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

func mustDialAndExpectCloseWithError(t *testing.T, endpoint string, code int, reason string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/%s", getServerURLWS(), endpoint)
	conn, _, err := testDialer.DialContext(ctx, url, nil)
	if err != nil {
		t.Fatalf("Failed to dial %q: %v", url, err)
	}
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Expected close error, got %v", err)
	}
	if closeErr.Code != code {
		t.Fatalf("Expected code %d, got %d", code, closeErr.Code)
	}
	if closeErr.Text != reason {
		t.Fatalf("Expected reason %q, got %q", reason, closeErr.Text)
	}
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

var testDialer = websocket.Dialer{
	Subprotocols:     []string{"2FAS-Pass"},
	ReadBufferSize:   1024,
	WriteBufferSize:  1024,
	HandshakeTimeout: 30 * time.Second,
}
