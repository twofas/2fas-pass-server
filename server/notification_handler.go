// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"firebase.google.com/go/v4/messaging"
	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/twofas/2fas-pass-server/server/fcm"
	"github.com/twofas/2fas-pass-server/server/notif"
)

type NotificationHandler struct {
	Logger         *slog.Logger
	FCMClient      fcm.Client
	Notifications  *notif.Service
	TTL            time.Duration
	MaxMessageSize int64
}

func (h *NotificationHandler) Add(writer http.ResponseWriter, request *http.Request) {
	var req PushToMobileRequest

	r := &io.LimitedReader{R: request.Body, N: h.MaxMessageSize}
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		if r.N <= 0 {
			writeError(h.Logger, writer, http.StatusRequestEntityTooLarge, "Request too large")
			return
		}
		writeError(h.Logger, writer, http.StatusBadRequest, "Invalid json payload")
		return
	}
	if err := validatePushToMobileRequest(req); err != nil {
		writeError(h.Logger, writer, http.StatusBadRequest, err.Error())
		return
	}

	notificationID := uuid.NewString()
	if req.FCMToken != "" {
		msg, err := makeMessage(req, notificationID)
		if err != nil {
			writeError(h.Logger, writer, http.StatusBadRequest, err.Error())
			return
		}

		// We don't want to cancel push request, because client cancelled their request.
		// Hence, we use context.Background() instead of request.Context().
		resp, err := h.FCMClient.Send(context.Background(), msg) //nolint:contextcheck
		if err != nil {
			writeFCMError(h.Logger, writer, err)
			return
		}
		h.Logger.Debug("Sent push message", slog.String("response", string(resp)))
	}
	n := notif.Notification{
		ID:        notificationID,
		Data:      req.Data,
		ExpiresAt: time.Now().Add(h.TTL),
	}
	h.Notifications.Add(req.DeviceID, n, h.TTL)

	response := struct {
		NotificationID string `json:"notificationId"`
	}{
		NotificationID: notificationID,
	}
	bb, err := json.Marshal(response)
	if err != nil {
		h.Logger.Error("Failed to marshal response", slog.Any("error", err))
		writeError(h.Logger, writer, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSONContent(writer, http.StatusOK, bb)
}

func (h *NotificationHandler) Get(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	deviceID := vars["device_id"]
	notifications := h.Notifications.Get(deviceID, time.Now())
	response := struct {
		Notifications []notif.Notification `json:"notifications"`
	}{
		Notifications: notifications,
	}
	bb, err := json.Marshal(response)
	if err != nil {
		writeError(h.Logger, w, http.StatusInternalServerError, "Internal server error")
		h.Logger.Error("Failed to marshal response", slog.Any("error", err))
		return
	}
	writeJSONContent(w, http.StatusOK, bb)
}

func (h *NotificationHandler) Delete(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	deviceID := vars["device_id"]
	notificationID := vars["notification_id"]

	ok := h.Notifications.Delete(deviceID, notificationID)
	if ok {
		writeJSONContent(w, http.StatusOK, `{"status": "deleted"}`)
	} else {
		writeError(h.Logger, w, http.StatusNotFound, "notification not found")
	}
}

func makeMessage(req PushToMobileRequest, notificationID string) (*messaging.Message, error) {
	tokenPushNotificationTTL := time.Minute * 3
	data := req.Data
	data["notificationId"] = notificationID

	switch req.FCMTargetType {
	case "android", "": // as Android as the default value for the field FCMTargetType.
		return &messaging.Message{
			Token: req.FCMToken,
			Android: &messaging.AndroidConfig{
				Data: data,
				TTL:  &tokenPushNotificationTTL,
			},
		}, nil
	case "ios":
		ttl := time.Now().Add(tokenPushNotificationTTL)

		return &messaging.Message{
			APNS: &messaging.APNSConfig{
				Headers: map[string]string{
					"apns-expiration": fmt.Sprintf("%d", ttl.Unix()),
				},
				Payload: &messaging.APNSPayload{
					Aps: &messaging.Aps{
						MutableContent: true,
						Sound:          "default",
						Alert: &messaging.ApsAlert{
							Title: "2FAS Pass request",
							Body:  "New request from browser",
						},
					},
					CustomData: adaptDataForIOS(data),
				},
			},
			Token: req.FCMToken,
		}, nil
	default:
		return nil, fmt.Errorf("unknown fcm target type: %q", req.FCMTargetType)
	}
}

func adaptDataForIOS(m map[string]string) map[string]interface{} {
	res := make(map[string]interface{}, len(m))
	for k, v := range m {
		res[k] = v
	}
	return res
}

func validatePushToMobileRequest(req PushToMobileRequest) error {
	if req.DeviceID == "" {
		return fmt.Errorf("deviceID is required")
	}
	if len(req.FCMToken) > 256 {
		return fmt.Errorf("fcmToken is too long")
	}
	if len(req.DeviceID) > 256 {
		return fmt.Errorf("deviceID is too long")
	}
	if len(req.Data) == 0 {
		return fmt.Errorf("data is required")
	}
	// Max Data size is limited, because we use LimitedReader when decoding JSON.
	return nil
}

func writeFCMError(logger *slog.Logger, w http.ResponseWriter, err error) {
	var fcmErr fcm.Error
	if !errors.As(err, &fcmErr) {
		logger.Error("Failed to send push message", slog.Any("error", err))
		writeError(logger, w, http.StatusInternalServerError, "Failed to send push message")
	}
	switch fcmErr.Typ { //nolint:exhaustive // There is a default case.
	case fcm.ErrorInvalidArgument, fcm.ErrorSenderIDMismatch, fcm.ErrorUnregistered:
		writeFCMErrorPayload(logger, w, fcmErr)
	default:
		logger.Error("Failed to send push message",
			slog.Any("error", err),
			slog.String("fcm_error_type", string(fcmErr.Typ)))
		writeError(logger, w, http.StatusInternalServerError, "Failed to send push message")
	}
}

func writeFCMErrorPayload(logger *slog.Logger, w http.ResponseWriter, fcmErr fcm.Error) {
	payload := struct {
		Error             string `json:"error"`
		FirebaseError     string `json:"firebaseError"`
		FirebaseErrorCode string `json:"firebaseErrorCode"`
	}{
		Error:             "Invalid request: failed to send push message",
		FirebaseError:     fcmErr.Error(),
		FirebaseErrorCode: string(fcmErr.Typ),
	}
	bb, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal response error payload", slog.Any("error", err))
		writeJSONContent(w, http.StatusInternalServerError, `{"error": "Internal server error"}`)
		return
	}
	writeJSONContent(w, http.StatusBadRequest, bb)
}
