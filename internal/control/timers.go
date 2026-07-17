package control

import (
	"context"
	"fmt"

	"github.com/keithah/openwrt-wattline/internal/proto"
)

func (s *Service) GetUSBCLimit(ctx context.Context, typ int) (int, error) {
	if typ < proto.LimitGlobal || typ > proto.LimitRuntime {
		return 0, fmt.Errorf("invalid USB-C limit type %d", typ)
	}
	session, err := s.sessionFor(ctx, supportsLimits, false)
	if err != nil {
		return 0, err
	}
	return session.GetUSBCLimit(typ)
}

func (s *Service) PutUSBCLimit(ctx context.Context, typ, level int) (int, error) {
	if err := proto.ValidateLimitWrite(byte(typ), level); err != nil {
		return 0, err
	}
	session, err := s.sessionFor(ctx, supportsLimits, false)
	if err != nil {
		return 0, err
	}
	return session.PutUSBCLimit(typ, level)
}

func (s *Service) DeleteUSBCLimit(ctx context.Context, typ int) (int, error) {
	if typ < proto.LimitGlobal || typ > proto.LimitOutput {
		return 0, fmt.Errorf("invalid mutable USB-C limit type %d", typ)
	}
	session, err := s.sessionFor(ctx, supportsLimits, false)
	if err != nil {
		return 0, err
	}
	return session.DeleteUSBCLimit(typ)
}

func (s *Service) ListTimers(ctx context.Context) ([]proto.Timer, error) {
	session, err := s.sessionFor(ctx, supportsTimers, false)
	if err != nil {
		return nil, err
	}
	return session.ListTimers()
}

func (s *Service) GetTimer(ctx context.Context, id byte) (proto.Timer, error) {
	if id == 0xff {
		return proto.Timer{}, fmt.Errorf("timer ID 255 is reserved for add")
	}
	timers, err := s.ListTimers(ctx)
	if err != nil {
		return proto.Timer{}, err
	}
	for _, timer := range timers {
		if timer.ID == id {
			return timer, nil
		}
	}
	return proto.Timer{}, ErrNotFound
}

func (s *Service) AddTimer(ctx context.Context, timer proto.Timer) ([]proto.Timer, byte, error) {
	if err := proto.ValidateTimerWrite(timer); err != nil {
		return nil, 0, err
	}
	session, err := s.sessionFor(ctx, supportsTimers, false)
	if err != nil {
		return nil, 0, err
	}
	return session.AddTimer(timer)
}

func (s *Service) PutTimer(ctx context.Context, id byte, timer proto.Timer) ([]proto.Timer, error) {
	if id == 0xff {
		return nil, fmt.Errorf("timer ID 255 is reserved for add")
	}
	if err := proto.ValidateTimerWrite(timer); err != nil {
		return nil, err
	}
	session, err := s.sessionFor(ctx, supportsTimers, false)
	if err != nil {
		return nil, err
	}
	return session.PutTimer(id, timer)
}

func (s *Service) DeleteTimer(ctx context.Context, id byte) ([]proto.Timer, error) {
	if id == 0xff {
		return nil, fmt.Errorf("timer ID 255 is reserved for add")
	}
	session, err := s.sessionFor(ctx, supportsTimers, false)
	if err != nil {
		return nil, err
	}
	return session.DeleteTimer(id)
}
