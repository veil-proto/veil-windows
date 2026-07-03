//go:build windows

// Package wintunnel is the Windows tunnel controller: it brings a VEIL tunnel up
// and down from config text and reports its live status. It is the backend the
// veil-service control channel drives (it implements control.Handler), factored
// out so the service main stays about the Service Control Manager and nothing
// else.
package wintunnel

import (
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/veil-proto/veil/config"
	"github.com/veil-proto/veil-windows/control"
	"github.com/veil-proto/veil/engine"
	"github.com/veil-proto/veil-windows/windev"
	"github.com/veil-proto/veil-windows/winipcfg"
)

// Tunnel owns at most one live VEIL tunnel. All methods are safe for concurrent
// use; Start while already running switches configs (tears the old one down
// first). It implements control.Handler.
type Tunnel struct {
	mu      sync.Mutex
	running bool
	gen     uint64 // bumped each Start, so a stale engine-error goroutine can't tear down a newer session
	name    string
	iface   string

	eng     *engine.Engine
	tun     *windev.TunDevice
	conn    *net.UDPConn
	cleanup func()
}

// Connect brings up (or switches to) the config in configText under a display
// name. Satisfies control.Handler.
func (t *Tunnel) Connect(configText, name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		t.stopLocked()
	}

	cfg, err := config.LoadConfigString(configText)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	tunDev, err := windev.NewTunDevice("veil0")
	if err != nil {
		return fmt.Errorf("create adapter: %w", err)
	}
	conn, err := bindUDP(cfg)
	if err != nil {
		tunDev.Close()
		return fmt.Errorf("bind udp: %w", err)
	}
	tuneUDPSocket(conn)

	cleanup, err := windev.ConfigureRouting(winipcfg.LUID(tunDev.LUID()), cfg)
	if err != nil {
		conn.Close()
		tunDev.Close()
		return fmt.Errorf("configure routing: %w", err)
	}

	eng, err := engine.New(cfg, tunDev, conn)
	if err != nil {
		cleanup()
		conn.Close()
		tunDev.Close()
		return fmt.Errorf("build engine: %w", err)
	}

	errChan := make(chan error, 2)
	eng.Run(errChan)

	t.eng, t.tun, t.conn, t.cleanup = eng, tunDev, conn, cleanup
	t.name, t.iface, t.running = name, tunDev.Name(), true
	t.gen++
	gen := t.gen

	// A fatal engine loop error (e.g. the TUN/UDP handle closed unexpectedly)
	// tears this session down — but only if it's still the current one, so a
	// quick config switch isn't undone by the old session's dying goroutine.
	go func() {
		if err := <-errChan; err != nil {
			t.mu.Lock()
			if t.running && t.gen == gen {
				log.Printf("tunnel %q engine error: %v", name, err)
				t.stopLocked()
			}
			t.mu.Unlock()
		}
	}()

	log.Printf("tunnel %q up on %s", name, t.iface)
	return nil
}

// Disconnect tears the tunnel down. Safe to call when already disconnected.
func (t *Tunnel) Disconnect() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopLocked()
	return nil
}

func (t *Tunnel) stopLocked() {
	if !t.running {
		return
	}
	// Bump gen first so the outgoing session's error goroutine becomes a no-op.
	t.gen++
	if t.cleanup != nil {
		t.cleanup()
	}
	if t.conn != nil {
		t.conn.Close()
	}
	if t.tun != nil {
		t.tun.Close()
	}
	name := t.name
	t.eng, t.conn, t.tun, t.cleanup = nil, nil, nil, nil
	t.running, t.name, t.iface = false, "", ""
	log.Printf("tunnel %q down", name)
}

// Status reports the current tunnel state for the control channel.
func (t *Tunnel) Status() control.Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running || t.eng == nil {
		return control.Status{State: control.StateDisconnected}
	}

	st := control.Status{State: control.StateConnected, Name: t.name, Iface: t.iface}
	anyConnected := false
	for _, ps := range t.eng.Stats() {
		p := control.PeerStatus{
			PublicKey:   ps.PublicKey,
			Endpoint:    ps.Endpoint,
			Connected:   ps.Connected,
			RxBytes:     ps.RxBytes,
			TxBytes:     ps.TxBytes,
			FrameBudget: ps.FrameBudget,
		}
		if !ps.LastHandshake.IsZero() {
			p.LastHandshake = ps.LastHandshake.Unix()
		}
		if !ps.LastReceived.IsZero() {
			p.LastReceived = ps.LastReceived.Unix()
		}
		st.Peers = append(st.Peers, p)
		anyConnected = anyConnected || ps.Connected
	}
	// Up but nobody has completed a handshake yet: still connecting.
	if !anyConnected {
		st.State = control.StateConnecting
	}
	return st
}
