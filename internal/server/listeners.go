package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Endpoint struct {
	Enabled      bool
	Addr4, Addr6 string
	Port         int
}
type ListenerConfig struct {
	HTTP, HTTPS       Endpoint
	CertFile, KeyFile string
}

type Group struct {
	servers     []*http.Server
	listeners   []net.Listener
	errs        chan error
	done        chan struct{}
	once        sync.Once
	shutdownErr error
}

func Start(ctx context.Context, cfg ListenerConfig, handler http.Handler) (*Group, error) {
	if handler == nil {
		return nil, errors.New("HTTP handler is nil")
	}
	if !cfg.HTTP.Enabled && !cfg.HTTPS.Enabled {
		return nil, errors.New("no HTTP endpoint enabled")
	}
	if err := validateEndpoint("HTTP", cfg.HTTP); err != nil {
		return nil, err
	}
	if err := validateEndpoint("HTTPS", cfg.HTTPS); err != nil {
		return nil, err
	}
	var tlsConfig *tls.Config
	if cfg.HTTPS.Enabled {
		pair, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load HTTPS key pair: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	}
	type pending struct {
		listener net.Listener
		tls      bool
	}
	var opened []pending
	closeOpened := func() {
		for _, item := range opened {
			_ = item.listener.Close()
		}
	}
	for _, spec := range []struct {
		name   string
		ep     Endpoint
		secure bool
	}{{"HTTP", cfg.HTTP, false}, {"HTTPS", cfg.HTTPS, true}} {
		if !spec.ep.Enabled {
			continue
		}
		for _, address := range []struct{ network, host string }{{"tcp4", spec.ep.Addr4}, {"tcp6", spec.ep.Addr6}} {
			if address.host == "" {
				continue
			}
			listener, err := listen(ctx, address.network, address.host, spec.ep.Port)
			if err != nil {
				closeOpened()
				return nil, fmt.Errorf("bind %s %s: %w", spec.name, address.network, err)
			}
			opened = append(opened, pending{listener, spec.secure})
		}
	}
	g := &Group{errs: make(chan error, len(opened)), done: make(chan struct{})}
	for _, item := range opened {
		listener := item.listener
		if item.tls {
			listener = tls.NewListener(listener, tlsConfig.Clone())
		}
		srv := &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       15 * time.Second,
		}
		g.listeners = append(g.listeners, listener)
		g.servers = append(g.servers, srv)
		go func(server *http.Server, l net.Listener) {
			if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				select {
				case g.errs <- err:
				default:
				}
			}
		}(srv, listener)
	}
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = g.Shutdown(shutdownCtx)
			cancel()
		case <-g.done:
		}
	}()
	return g, nil
}

func validateEndpoint(name string, ep Endpoint) error {
	if !ep.Enabled {
		return nil
	}
	if ep.Port < 1 || ep.Port > 65535 {
		return fmt.Errorf("%s port is invalid", name)
	}
	if ep.Addr4 == "" && ep.Addr6 == "" {
		return fmt.Errorf("%s endpoint has no address", name)
	}
	return nil
}

func listen(ctx context.Context, network, host string, port int) (net.Listener, error) {
	lc := net.ListenConfig{}
	if network == "tcp6" {
		lc.Control = func(_, _ string, raw syscall.RawConn) error {
			var optionErr error
			if err := raw.Control(func(fd uintptr) {
				optionErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, 1)
			}); err != nil {
				return err
			}
			return optionErr
		}
	}
	return lc.Listen(ctx, network, net.JoinHostPort(host, strconv.Itoa(port)))
}

func (g *Group) Errors() <-chan error { return g.errs }

func (g *Group) Shutdown(ctx context.Context) error {
	g.once.Do(func() {
		for _, srv := range g.servers {
			if err := srv.Shutdown(ctx); err != nil {
				if g.shutdownErr == nil {
					g.shutdownErr = err
				}
				_ = srv.Close()
			}
		}
		for _, listener := range g.listeners {
			_ = listener.Close()
		}
		close(g.done)
	})
	return g.shutdownErr
}
