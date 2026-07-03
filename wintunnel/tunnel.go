//go:build windows

// Package wintunnel is the Windows tunnel controller: it brings a VEIL tunnel up
// and down from config text and reports its live status. It is the backend the
// veil-service control channel drives (it implements control.Handler), factored
// out so the service main stays about the Service Control Manager and nothing
// else.
package wintunnel

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/veil-proto/veil-windows/control"
	"github.com/veil-proto/veil-windows/windev"
	"github.com/veil-proto/veil-windows/winipcfg"
	"github.com/veil-proto/veil/config"
	"github.com/veil-proto/veil/engine"
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
	// done is closed by stopLocked once the engine has fully stopped (after
	// eng.Close/Wait), so the errChan-watcher goroutine below can exit
	// instead of blocking forever: a clean stop no longer writes to errChan
	// at all (Engine only reports genuine fatal loop errors there now), so
	// without this the watcher would leak on every Connect/Disconnect cycle.
	done chan struct{}

	// logs is the process-lifetime log ring buffer backing the Logs control
	// command. It is independent of running/gen/eng so captured lines span
	// tunnel restarts rather than resetting on every Connect/Disconnect. It
	// is lazily created on first use so a zero-value Tunnel{} (as built by
	// cmd/veil-service) works without an explicit constructor; the caller is
	// still expected to route the process's log output into it (e.g. via
	// log.SetOutput(io.MultiWriter(os.Stderr, t.LogBuffer()))) for lines to
	// actually show up.
	logsOnce sync.Once
	logs     *control.LogBuffer
}

// LogBuffer returns the tunnel's log ring buffer, creating it on first call.
// The caller (cmd/veil-service) is expected to plug this into log.SetOutput
// at startup so existing log.Printf call sites across the engine/service code
// get captured without any call-site changes.
func (t *Tunnel) LogBuffer() *control.LogBuffer {
	t.logsOnce.Do(func() { t.logs = control.NewLogBuffer(0) })
	return t.logs
}

// Logs returns every captured log line with Seq > since. Satisfies
// control.Handler.
func (t *Tunnel) Logs(since uint64) []control.LogLine {
	return t.LogBuffer().Since(since)
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
	done := make(chan struct{})
	eng.Run(context.Background(), errChan)

	t.eng, t.tun, t.conn, t.cleanup = eng, tunDev, conn, cleanup
	t.name, t.iface, t.running = name, tunDev.Name(), true
	t.done = done
	t.gen++
	gen := t.gen

	// A fatal engine loop error (e.g. the TUN/UDP handle closed unexpectedly)
	// tears this session down — but only if it's still the current one, so a
	// quick config switch isn't undone by the old session's dying goroutine.
	// A clean stop (stopLocked) closes done instead of writing to errChan, so
	// this goroutine always has an exit path and never leaks.
	go func() {
		select {
		case err := <-errChan:
			if err == nil {
				return
			}
			t.mu.Lock()
			if t.running && t.gen == gen {
				log.Printf("tunnel %q engine error: %v", name, err)
				t.stopLocked()
			}
			t.mu.Unlock()
		case <-done:
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
	// Close signals the data-plane loops to stop; closing conn/tun is what
	// actually unblocks their in-flight blocking reads (Close alone doesn't).
	// Wait only returns once every loop has exited, so it must come after
	// both closes — call it before returning so a caller that immediately
	// reconnects never races a not-yet-dead previous session.
	if t.eng != nil {
		t.eng.Close()
	}
	if t.cleanup != nil {
		t.cleanup()
	}
	if t.conn != nil {
		t.conn.Close()
	}
	if t.tun != nil {
		t.tun.Close()
	}
	if t.eng != nil {
		t.eng.Wait()
	}
	if t.done != nil {
		close(t.done)
	}
	name := t.name
	t.eng, t.conn, t.tun, t.cleanup, t.done = nil, nil, nil, nil, nil
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
