package ble

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

const restartReconnectDelay = 15 * time.Second

type lifecyclePolicy interface {
	ArmReconnect(time.Duration)
	DisarmReconnect()
	ResumeReconnect()
}

func (s *Session) ReadClock() (time.Time, bool, error) {
	if !s.HasChar(CharTime) {
		return time.Time{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.t.ReadChar(CharTime)
	if err != nil {
		return time.Time{}, true, err
	}
	if len(b) < 10 {
		return time.Time{}, true, fmt.Errorf("current time payload too short: % x", b)
	}
	year := int(b[0]) | int(b[1])<<8
	month, day := time.Month(b[2]), int(b[3])
	hour, minute, second := int(b[4]), int(b[5]), int(b[6])
	if year == 0 || month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, true, fmt.Errorf("invalid current time payload: % x", b)
	}
	nsec := int(uint64(b[8]) * uint64(time.Second) / 256)
	got := time.Date(year, month, day, hour, minute, second, nsec, time.Local)
	if got.Year() != year || got.Month() != month || got.Day() != day {
		return time.Time{}, true, fmt.Errorf("invalid current time payload: % x", b)
	}
	return got, true, nil
}

func (s *Session) SyncClock(t time.Time, reason byte) error {
	if !s.HasChar(CharTime) {
		return fmt.Errorf("current time characteristic unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.t.WriteChar(CharTime, proto.CurrentTimeAt(t, reason))
}

func (s *Session) OTAInfo() (proto.OTAInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.t.WriteChar(CharOTA, proto.OTAInfoQuery()); err != nil {
		return proto.OTAInfo{}, err
	}
	reply, err := s.t.ReadChar(CharOTA)
	if err != nil {
		return proto.OTAInfo{}, err
	}
	return proto.ParseOTAInfo(reply)
}

func (s *Session) waitForDisconnect(ctx context.Context, uuid string, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	disconnected := s.t.Disconnected()
	select {
	case <-disconnected:
		return errors.New("device was already disconnected before lifecycle operation")
	default:
	}
	writeErr := s.t.WriteChar(uuid, frame)
	timer := time.NewTimer(s.disconnectGrace)
	defer timer.Stop()
	select {
	case <-disconnected:
		s.store.SetConnected(false)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if writeErr != nil {
			return writeErr
		}
		return errors.New("device did not disconnect within lifecycle grace period")
	}
}

func (s *Session) EnterOTA(ctx context.Context) error {
	if s.lifecycle != nil {
		s.lifecycle.ArmReconnect(0)
	}
	err := s.waitForDisconnect(ctx, CharOTA, proto.OTAEnter())
	if err != nil && s.lifecycle != nil {
		s.lifecycle.ResumeReconnect()
	}
	return err
}

func (s *Session) ExitOTA(ctx context.Context) error {
	if s.lifecycle != nil {
		s.lifecycle.ArmReconnect(0)
	}
	err := s.waitForDisconnect(ctx, CharOTA, proto.OTAExit())
	if err != nil && s.lifecycle != nil {
		s.lifecycle.ResumeReconnect()
	}
	return err
}

func (s *Session) Restart() error {
	if s.lifecycle != nil {
		s.lifecycle.ArmReconnect(restartReconnectDelay)
	}
	err := s.waitForDisconnect(context.Background(), CharCmd, proto.Restart())
	if err != nil && s.lifecycle != nil {
		s.lifecycle.ResumeReconnect()
	}
	return err
}

func (s *Session) Shutdown() error {
	if s.lifecycle != nil {
		s.lifecycle.DisarmReconnect()
	}
	err := s.waitForDisconnect(context.Background(), CharFactory, proto.ShutdownMagic())
	if err != nil && s.lifecycle != nil {
		s.lifecycle.ResumeReconnect()
	}
	return err
}
