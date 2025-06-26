// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package wstunnel

import (
	"sync"
)

// tunnelClient is enum with values for "left" and "right".
type tunnelClient int

const (
	tunnelDirectionLeft  tunnelClient = 0
	tunnelDirectionRight tunnelClient = 1
)

// tunnelState keeps track on who connected to the tunnel.
type tunnelState struct {
	mtx                        *sync.Mutex
	isClientConnected          [2]bool
	firstClientStoppedDraining chan struct{}
	allClientsConnected        chan struct{}
}

func newTunnelState() *tunnelState {
	ws := &tunnelState{
		mtx:                        &sync.Mutex{},
		isClientConnected:          [2]bool{false, false},
		firstClientStoppedDraining: make(chan struct{}),
		allClientsConnected:        make(chan struct{}),
	}
	return ws
}

type clientOrder int

const (
	clientConnectedFirst  clientOrder = 1
	clientConnectedSecond clientOrder = 2
)

// onClientConnected is called when a client connects to the tunnel.
// Methods updates the state and returns information about whether the client is the first or second to connect.
func (t *tunnelState) onClientConnected(this tunnelClient) (clientOrder, error) {
	t.mtx.Lock()
	defer t.mtx.Unlock()

	if t.isClientConnected[this] {
		return 0, ErrAlreadyConnected
	}
	t.isClientConnected[this] = true

	other := (this + 1) % 2
	if t.isClientConnected[other] {
		close(t.allClientsConnected)
		return clientConnectedSecond, nil
	}
	return clientConnectedFirst, nil
}

// onFirstClientStoppedDraining is called when the first client stopped draining messages.
func (t *tunnelState) onFirstClientStoppedDraining() {
	close(t.firstClientStoppedDraining)
}
