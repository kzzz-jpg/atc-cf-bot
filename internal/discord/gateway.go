package discord

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type rootTransport struct {
	ctx       context.Context
	transport http.RoundTripper
}

func (t rootTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(r.WithContext(t.ctx))
}

type rootConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *rootConn) Close() error { c.once.Do(func() { close(c.done) }); return c.Conn.Close() }

// OpenGateway ties SDK HTTP, handshake, and socket lifetime to the application.
// The first Hello/Ready read has a deadline; live heartbeat reads then use SDK
// handling while root cancellation still closes the underlying connection.
func (a *Adapter) OpenGateway(ctx context.Context) error {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	a.Session.Client = &http.Client{Transport: rootTransport{ctx: ctx, transport: tr}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dialer := *a.Session.Dialer
	dialer.HandshakeTimeout = 15 * time.Second
	var mu sync.Mutex
	connections := map[*rootConn]bool{}
	dialer.NetDialContext = func(_ context.Context, network, address string) (net.Conn, error) {
		base, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, address)
		if e != nil {
			return nil, e
		}
		c := &rootConn{Conn: base, done: make(chan struct{})}
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		mu.Lock()
		connections[c] = true
		mu.Unlock()
		go func() {
			select {
			case <-ctx.Done():
				_ = c.Close()
			case <-c.done:
			}
			mu.Lock()
			delete(connections, c)
			mu.Unlock()
		}()
		return c, nil
	}
	a.Session.Dialer = &dialer
	clear := func() {
		mu.Lock()
		defer mu.Unlock()
		for c := range connections {
			_ = c.SetDeadline(time.Time{})
		}
	}
	a.Session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { clear() })
	a.Session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { clear() })
	if e := a.Session.Open(); e != nil {
		return domain.Fail(domain.Transient)
	}
	clear()
	return nil
}
