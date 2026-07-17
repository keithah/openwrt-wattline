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
	value, err := session.GetUSBCLimit(typ)
	return value, wrapBLE(err)
}

func (s *Service) PutUSBCLimit(ctx context.Context, typ, level int) (int, error) {
	if typ < proto.LimitGlobal || typ > proto.LimitOutput {
		return 0, fmt.Errorf("invalid mutable USB-C limit type %d", typ)
	}
	if err := proto.ValidateLimitWrite(byte(typ), level); err != nil {
		return 0, err
	}
	session, err := s.sessionFor(ctx, supportsLimits, false)
	if err != nil {
		return 0, err
	}
	value, err := session.PutUSBCLimit(typ, level)
	return value, wrapBLE(err)
}

func (s *Service) DeleteUSBCLimit(ctx context.Context, typ int) (int, error) {
	if typ < proto.LimitGlobal || typ > proto.LimitOutput {
		return 0, fmt.Errorf("invalid mutable USB-C limit type %d", typ)
	}
	session, err := s.sessionFor(ctx, supportsLimits, false)
	if err != nil {
		return 0, err
	}
	value, err := session.DeleteUSBCLimit(typ)
	return value, wrapBLE(err)
}

func (s *Service) ListTimers(ctx context.Context) ([]proto.Timer, error) {
	session, err := s.sessionFor(ctx, supportsTimers, false)
	if err != nil {
		return nil, err
	}
	timers, err := session.ListTimers()
	return timers, wrapBLE(err)
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
	timers, id, err := session.AddTimer(timer)
	return timers, id, wrapBLE(err)
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
	timers, err := session.PutTimer(id, timer)
	return timers, wrapBLE(err)
}

func (s *Service) DeleteTimer(ctx context.Context, id byte) ([]proto.Timer, error) {
	if id == 0xff {
		return nil, fmt.Errorf("timer ID 255 is reserved for add")
	}
	session, err := s.sessionFor(ctx, supportsTimers, false)
	if err != nil {
		return nil, err
	}
	timers, err := session.DeleteTimer(id)
	return timers, wrapBLE(err)
}
