// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"os"

	"firebase.google.com/go/v4/messaging"

	"github.com/twofas/2fas-pass-server/server/fcm"
)

func main() {
	credentialsFile := flag.String("credentials", "", "Path to the FCM credentials file")
	fcmToken := flag.String("token", "", "FCM token to send a message to")
	flag.Parse()

	if *credentialsFile == "" {
		log.Fatal("FCM credentials file is required")
	}
	if *fcmToken == "" {
		log.Fatal("FCM token is required")
	}

	credentials, err := os.ReadFile(*credentialsFile)
	if err != nil {
		log.Fatalf("Failed to read credentials file: %v", err)
	}

	client, err := fcm.NewClient(context.Background(), string(credentials), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		log.Fatalf("Failed to create FCM client: %v", err)
	}

	resp, err := client.Send(context.Background(), &messaging.Message{
		Token: *fcmToken,
		Android: &messaging.AndroidConfig{
			Data: map[string]string{
				"key": "value",
			},
		},
	})
	if err != nil {
		var fcmErr fcm.Error
		if errors.As(err, &fcmErr) {
			log.Printf("Failed to send push message: %v %s", fcmErr, fcmErr.Typ)
		} else {
			log.Printf("Failed to send push message: %v", err)
		}
	}

	log.Printf("Sent push message, response: %+v", resp)
}
