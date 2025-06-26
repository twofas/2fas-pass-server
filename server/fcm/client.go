// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package fcm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/avast/retry-go"
	"google.golang.org/api/option"
)

type ErrorType string

const (
	ErrorInternalError       ErrorType = "INTERNAL"
	ErrorThirdPartyAuthError ErrorType = "THIRD_PARTY_AUTH_ERROR"
	ErrorInvalidArgument     ErrorType = "INVALID_ARGUMENT"
	ErrorQuotaExceeded       ErrorType = "QUOTA_EXCEEDED"
	ErrorSenderIDMismatch    ErrorType = "SENDER_ID_MISMATCH"
	ErrorUnregistered        ErrorType = "UNREGISTERED"
	ErrorUnavailable         ErrorType = "UNAVAILABLE"
)

var nonRetryableErrors = map[ErrorType]struct{}{
	ErrorThirdPartyAuthError: {},
	ErrorInvalidArgument:     {},
	ErrorSenderIDMismatch:    {},
	ErrorUnregistered:        {},
}

type Error struct {
	Err error
	Typ ErrorType
}

func (e Error) Error() string {
	return e.Err.Error()
}

func makeFCMErrorType(err error) ErrorType {
	switch {
	case messaging.IsUnavailable(err):
		return ErrorUnavailable
	case messaging.IsUnregistered(err):
		return ErrorUnregistered
	case messaging.IsSenderIDMismatch(err):
		return ErrorSenderIDMismatch
	case messaging.IsQuotaExceeded(err):
		return ErrorQuotaExceeded
	case messaging.IsInvalidArgument(err):
		return ErrorInvalidArgument
	case messaging.IsThirdPartyAuthError(err):
		return ErrorThirdPartyAuthError
	case messaging.IsInternal(err):
		return ErrorInternalError
	default:
		return ""
	}
}

// Response is returned from firebsase for push request.
type Response string

type Client interface {
	Send(ctx context.Context, message *messaging.Message) (Response, error)
}

type client struct {
	FcmMessaging *messaging.Client
	logger       *slog.Logger
}

func NewClient(ctx context.Context, credentials string, logger *slog.Logger) (Client, error) {
	opt := option.WithCredentialsJSON([]byte(credentials))
	app, err := firebase.NewApp(ctx, nil, opt)
	if err != nil {
		return nil, fmt.Errorf("failed to create firebase app: %w", err)
	}
	fcmClient, err := app.Messaging(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create fcm client: %w", err)
	}
	return &client{FcmMessaging: fcmClient, logger: logger}, nil
}

func (c *client) Send(ctx context.Context, message *messaging.Message) (Response, error) {
	contextWithTimeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var response string
	err := retry.Do(func() error {
		var err error
		response, err = c.FcmMessaging.Send(contextWithTimeout, message)
		if err != nil {
			fcmErr := Error{
				Err: err,
				Typ: makeFCMErrorType(err),
			}
			resultErr := fmt.Errorf("failed to send push message: %w", fcmErr)
			if _, ok := nonRetryableErrors[fcmErr.Typ]; ok {
				return retry.Unrecoverable(resultErr)
			}
			return resultErr
		}
		return nil
	},
		retry.Context(ctx),
		retry.DelayType(retry.BackOffDelay),
		retry.OnRetry(func(n uint, err error) {
			c.logger.Info("Failed to send push message, retrying",
				slog.Any("error", err),
				slog.Uint64("retry", uint64(n)))
		}),
		retry.LastErrorOnly(true),
	)
	if err != nil {
		return "", err //nolint:wrapcheck // It's the error we returned from RetryableFunc in retry.Do.
	}

	return Response(response), nil
}

type FakePushClient struct {
	logger *slog.Logger
}

func NewFakePushClient(logger *slog.Logger) *FakePushClient {
	return &FakePushClient{
		logger: logger,
	}
}

func (p *FakePushClient) Send(_ context.Context, message *messaging.Message) (Response, error) {
	data, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("failed to marshal message: %w", err)
	}

	p.logger.Info("No sending push notifications, because Firebase is not enabled",
		slog.String("notification", string(data)))

	return "ok", nil
}
