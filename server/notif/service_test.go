// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package notif

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestSetStoresDataCorrectly(t *testing.T) {
	s := New()
	n := Notification{
		ID:        "testID",
		Data:      map[string]string{"key": "value"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.Add("deviceID", n, (1 * time.Hour))

	result := s.Get("deviceID", time.Now())
	expected := []Notification{n}
	if diff := cmp.Diff(expected, result); diff != "" {
		t.Fatalf("Unexpected result:\n%s", diff)
	}
}

func TestGetReturnsEmptyListForExpiredDevice(t *testing.T) {
	s := New()
	n := Notification{
		ID:        "testID",
		Data:      map[string]string{"key": "value"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.Add("deviceID", n, time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	s.deleteExpired()

	result := s.Get("deviceID", time.Now())
	if len(result) != 0 {
		t.Fatalf("expected empty list, got %v", result)
	}
}

func TestGetReturnsNilForExpiredNotification(t *testing.T) {
	s := New()
	n := Notification{
		ID:        "testID",
		Data:      map[string]string{"key": "value"},
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	s.Add("deviceID", n, time.Hour)

	result := s.Get("deviceID", time.Now())
	if len(result) != 0 {
		t.Fatalf("expected empty list, got %v", result)
	}
}

func TestGetReturnsEmptyListForNonExistentKey(t *testing.T) {
	s := New()

	result := s.Get("nonExistentKey", time.Now())
	if len(result) != 0 {
		t.Fatalf("expected empty list, got %v", result)
	}
}

func TestAddThreeNotificationsOneIsExpired(t *testing.T) {
	s := New()
	n1 := Notification{
		ID:        "1",
		Data:      map[string]string{"key": "1"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	n2 := Notification{
		ID:        "2",
		Data:      map[string]string{"key": "2"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	n3 := Notification{
		ID:        "3",
		Data:      map[string]string{"key": "3"},
		ExpiresAt: time.Now().Add(-time.Hour),
	}
	s.Add("deviceID", n1, time.Hour)
	s.Add("deviceID", n2, time.Hour)
	s.Add("deviceID", n3, time.Hour)

	result := s.Get("deviceID", time.Now())

	expected := []Notification{n1, n2}
	if diff := cmp.Diff(expected, result); diff != "" {
		t.Fatalf("Unexpected result:\n%s", diff)
	}
}

func TestAddDuplicatedNotificationID(t *testing.T) {
	s := New()
	n1 := Notification{
		ID:        "1",
		Data:      map[string]string{"key": "1"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	n2 := Notification{
		ID:        "2",
		Data:      map[string]string{"key": "2"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	n3 := Notification{
		ID:        "1",
		Data:      map[string]string{"key": "3"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	s.Add("deviceID", n1, time.Hour)
	s.Add("deviceID", n2, time.Hour)
	s.Add("deviceID", n3, time.Hour)

	result := s.Get("deviceID", time.Now())

	expected := []Notification{n2, n3}
	if diff := cmp.Diff(expected, result); diff != "" {
		t.Fatalf("Unexpected result:\n%s", diff)
	}
}

func TestConcurrentAdd(t *testing.T) {
	const concurrency = 10
	const iterations = 1000
	const devices = 5
	const expectedNotificationsPerDevice = iterations * concurrency / devices

	s := New()
	wg := &sync.WaitGroup{}
	wg.Add(concurrency)
	for i := range concurrency {
		go func(i int) {
			for j := range iterations {
				deviceID := j % devices
				n := Notification{
					ID:        fmt.Sprintf("%d-%d", i, j),
					Data:      map[string]string{"key": "value"},
					ExpiresAt: time.Now().Add(time.Hour),
				}
				s.Add(fmt.Sprint(deviceID), n, time.Hour)
			}
			wg.Done()
		}(i)
	}
	wg.Wait()

	for i := range devices {
		notifs := s.Get(fmt.Sprint(i), time.Now())
		if len(notifs) != expectedNotificationsPerDevice {
			t.Fatalf("expected %d notifications for device %d, got %d", expectedNotificationsPerDevice, i, len(notifs))
		}
	}
}

func TestDelete(t *testing.T) { //nolint:funlen // this is a table test
	const deviceID = "device_id"

	n1 := Notification{
		ID:        "notif_1",
		Data:      map[string]string{"key": "value1"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	n2 := Notification{
		ID:        "notif_2",
		Data:      map[string]string{"key": "value2"},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	n3 := Notification{
		ID:        "notif_3",
		Data:      map[string]string{"key": "value2"},
		ExpiresAt: time.Now().Add(-time.Hour),
	}

	createService := func() *Service {
		s := New()
		s.Add(deviceID, n1, time.Hour)
		s.Add(deviceID, n2, time.Hour)
		s.Add(deviceID, n3, time.Hour)
		return s
	}

	tests := []struct {
		name           string
		notificationID string
		expectedResult bool
		expectedCount  int
	}{
		{
			name:           "Deletes existing notification",
			notificationID: "notif_1",
			expectedResult: true,
			expectedCount:  1,
		},
		{
			name:           "Returns false for non-existent notification",
			notificationID: "nonExistentID",
			expectedResult: false,
			expectedCount:  2,
		},
		{
			name: "Returns true for expired notification " +
				"(this is just for documentation, there is business requirement for this)",
			notificationID: "notif_3",
			expectedResult: true,
			expectedCount:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := createService()

			result := s.Delete(deviceID, tt.notificationID)
			if result != tt.expectedResult {
				t.Fatalf("expected %v, got %v", tt.expectedResult, result)
			}

			notifications := s.Get(deviceID, time.Now())
			if len(notifications) != tt.expectedCount {
				t.Fatalf("expected %d notifications, got %d", tt.expectedCount, len(notifications))
			}
		})
	}
}

func TestConcurrencyAddListDelete(t *testing.T) {
	const iterations = 1000
	const devices = 3
	s := New()
	wg := &sync.WaitGroup{}

	addToDevice := func(deviceID string) {
		defer wg.Done()
		for i := range iterations {
			s.Add(deviceID, Notification{
				ID:        fmt.Sprint(i),
				Data:      map[string]string{"key": "value"},
				ExpiresAt: time.Now().Add(time.Hour),
			}, time.Hour)
		}
	}
	deleteFromDevice := func(deviceID string) {
		defer wg.Done()
		deleted := 0
		for deleted < iterations {
			list := s.Get(deviceID, time.Now())
			if len(list) == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			deleted++
			head := list[0]
			s.Delete(deviceID, head.ID)
		}
	}

	for i := range devices {
		wg.Add(2)

		deviceID := fmt.Sprint(i)
		go addToDevice(deviceID)
		go deleteFromDevice(deviceID)
	}

	wg.Wait()
	for i := range devices {
		deviceID := fmt.Sprint(i)
		list := s.Get(deviceID, time.Now())
		if len(list) != 0 {
			t.Fatalf("expected empty list, got %v", list)
		}
	}
}
