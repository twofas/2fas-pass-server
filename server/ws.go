// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package server

import (
	"fmt"
	"net/http"
	"net/textproto"
	"slices"
	"strings"

	"github.com/gorilla/websocket"
)

// supportedProtocol2pass is Protocol that will be sent back on upgrade mechanism.
const supportedProtocol2pass = "2FAS-Pass"

var protocolHeader = textproto.CanonicalMIMEHeaderKey("Sec-WebSocket-Protocol")

var upgrader2pass = websocket.Upgrader{
	ReadBufferSize:  4 * 1024,
	WriteBufferSize: 4 * 1024,
	// TODO: come back later and check how we want to validate origin.
	CheckOrigin: func(_ *http.Request) bool {
		return true
	},
	Subprotocols: []string{supportedProtocol2pass},
}

func upgrade(w http.ResponseWriter, req *http.Request) (*websocket.Conn, error) {
	protocols := strings.Split(req.Header.Get(protocolHeader), ",")
	if !slices.Contains(protocols, supportedProtocol2pass) {
		return nil, fmt.Errorf("upgrader not available for protocols: %v", protocols)
	}
	conn, err := upgrader2pass.Upgrade(w, req, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to upgrade proxy: %w", err)
	}
	return conn, nil
}
