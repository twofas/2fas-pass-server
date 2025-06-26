// SPDX-License-Identifier: BUSL-1.1
//
// Copyright © 2025 Two Factor Authentication Service, Inc.
// Licensed under the Business Source License 1.1
// See LICENSE file for full terms

package notif

import (
	"slices"
	"sync"
	"time"

	"github.com/jellydator/ttlcache/v3"
)

type device struct {
	Notifications []Notification
	Mtx           *sync.Mutex
}

func (d *device) appendNotification(n Notification) {
	d.Mtx.Lock()
	defer d.Mtx.Unlock()
	d.Notifications = slices.DeleteFunc(d.Notifications, func(n2 Notification) bool {
		return n2.ID == n.ID
	})
	d.Notifications = append(d.Notifications, n)
}

func (d *device) getNotifications() []Notification {
	d.Mtx.Lock()
	defer d.Mtx.Unlock()
	return append([]Notification{}, d.Notifications...)
}

type Notification struct {
	ID        string            `json:"id,omitempty"`
	Data      map[string]string `json:"data,omitempty"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// Service stores notifications in memory.
type Service struct {
	m *ttlcache.Cache[string, *device]
}

type Config struct{}

func New() *Service {
	s := &Service{
		m: ttlcache.New[string, *device](),
	}
	go s.m.Start()
	return s
}

func (s *Service) Add(deviceID string, n Notification, ttl time.Duration) {
	v, _ := s.m.GetOrSet(deviceID, &device{
		Mtx: &sync.Mutex{},
	}, ttlcache.WithTTL[string, *device](ttl))
	d := v.Value()
	d.appendNotification(n)
	s.m.Touch(deviceID)
}

func (s *Service) Get(deviceID string, now time.Time) []Notification {
	v := s.m.Get(deviceID)
	if v == nil {
		return nil
	}
	d := v.Value()
	notifs := d.getNotifications()
	return slices.DeleteFunc(notifs, func(n Notification) bool {
		return now.After(n.ExpiresAt)
	})
}

// deleteExpired is not exported, and it is only used in the tests.
func (s *Service) deleteExpired() {
	s.m.DeleteExpired()
}

func (s *Service) Delete(deviceID, notificationID string) bool {
	v := s.m.Get(deviceID)
	if v == nil {
		return false
	}

	d := v.Value()
	return d.deleteNotification(notificationID)
}

func (d *device) deleteNotification(id string) bool {
	d.Mtx.Lock()
	defer d.Mtx.Unlock()

	previousLen := len(d.Notifications)
	d.Notifications = slices.DeleteFunc(d.Notifications, func(n Notification) bool {
		return n.ID == id
	})
	return len(d.Notifications) != previousLen
}
