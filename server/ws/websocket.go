// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package ws

import (
	"encoding/binary"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Stats struct {
	CreatedAt    time.Time
	BytesRead    int64
	BytesWritten int64
}

type Conn interface {
	SetReadLimit(size int64)
	SetPongHandler(f func(string) error)
	SetReadDeadline(add time.Time) error
	ReadMessage() (messageType int, p []byte, err error)
	SetWriteDeadline(add time.Time) error
	WriteMessage(messageType int, data []byte) error
	Close() error
	SetPingHandler(h func(appData string) error)
	NextWriter(messageType int) (io.WriteCloser, error)
}

func NewMonitoredConn(
	conn *websocket.Conn,
	connectionID, direction string,
	logger *slog.Logger,
) *monitoredConn { //nolint:revive
	bytesWritten := int64(0)
	bytesRead := int64(0)
	return &monitoredConn{
		Conn:         conn,
		createdAt:    time.Now(),
		connectionID: connectionID,
		bytesWritten: &bytesWritten,
		bytesRead:    &bytesRead,
		logger:       logger,
		direction:    direction,
	}
}

type monitoredConn struct {
	*websocket.Conn
	createdAt    time.Time
	connectionID string
	bytesWritten *int64
	bytesRead    *int64
	logger       *slog.Logger
	direction    string
}

func (c *monitoredConn) WriteMessage(messageType int, data []byte) error {
	if messageType != websocket.CloseMessage {
		err := c.Conn.WriteMessage(messageType, data)
		if err == nil {
			atomic.AddInt64(c.bytesWritten, int64(len(data)))
		}
		return err //nolint:wrapcheck
	}
	closeCode := uint16(0)
	if len(data) >= 2 {
		closeCode = binary.BigEndian.Uint16(data)
	}
	err := c.Conn.WriteMessage(messageType, data)

	bytesWritten := atomic.LoadInt64(c.bytesWritten)
	bytesRead := atomic.LoadInt64(c.bytesRead)

	if err == nil {
		c.logger.Info("Sent close message",
			slog.Duration("age", time.Since(c.createdAt)),
			slog.String("connection_id", c.connectionID),
			slog.Int("closeCode", int(closeCode)),
			slog.String("direction", c.direction),
			slog.Int64("bytes_written", bytesWritten),
			slog.Int64("bytes_read", bytesRead),
		)
	} else {
		c.logger.Info("Failed to send close message",
			slog.Duration("age", time.Since(c.createdAt)),
			slog.String("connection_id", c.connectionID),
			slog.Int("closeCode", int(closeCode)),
			slog.String("direction", c.direction),
			slog.Int64("bytes_written", bytesWritten),
			slog.Int64("bytes_read", bytesRead),
			slog.Any("error", err),
		)
	}
	return err //nolint:wrapcheck
}

func (c *monitoredConn) ReadMessage() (int, []byte, error) {
	messageType, p, err := c.Conn.ReadMessage()
	if err == nil {
		atomic.AddInt64(c.bytesRead, int64(len(p)))
	}
	return messageType, p, err //nolint:wrapcheck
}

func (c *monitoredConn) NextWriter(messageType int) (io.WriteCloser, error) {
	w, err := c.Conn.NextWriter(messageType)
	if err != nil {
		return nil, err //nolint:wrapcheck
	}
	return &monitoredWriter{
		WriteCloser:     w,
		byteTransferred: c.bytesWritten,
	}, nil
}

type monitoredWriter struct {
	io.WriteCloser
	byteTransferred *int64
}

func (w *monitoredWriter) Write(p []byte) (int, error) {
	n, err := w.WriteCloser.Write(p)
	atomic.AddInt64(w.byteTransferred, int64(n))
	return n, err //nolint:wrapcheck
}

func (c *monitoredConn) Stats() Stats {
	return Stats{
		CreatedAt:    c.createdAt,
		BytesRead:    atomic.LoadInt64(c.bytesRead),
		BytesWritten: atomic.LoadInt64(c.bytesWritten),
	}
}
