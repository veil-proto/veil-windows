//go:build windows

// Command veil-client is the standalone Windows front-end for VEIL. It creates a
// VEIL TUN adapter (the VEIL-owned veiltun driver binding, not the vendored
// WireGuard layer), configures its address / routes / DNS via the shared engine
// routing helper (winipcfg LUID API), and runs the OS-independent data plane.
//
// It fixes the full-tunnel routing loop described in curent.md: before taking
// over the default route, windev.ConfigureRouting pins a /32 host route to the
// server endpoint via the physical default gateway, so the VPN's own encrypted
// UDP leaves through the real NIC instead of looping back into the tunnel.
package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/veil-proto/veil-windows/winipcfg"

	"github.com/veil-proto/veil/config"
	"github.com/veil-proto/veil/engine"
	"github.com/veil-proto/veil-windows/windev"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <config.ini>", os.Args[0])
	}

	cfg, err := config.LoadConfig(os.Args[1])
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	tunDev, err := windev.NewTunDevice("veil0")
	if err != nil {
		log.Fatalf("Failed to create VEIL adapter: %v", err)
	}
	defer tunDev.Close()

	luid := winipcfg.LUID(tunDev.LUID())
	log.Printf("VEIL adapter %s up (LUID %d)", tunDev.Name(), uint64(luid))

	conn, err := bindUDP(cfg)
	if err != nil {
		log.Fatalf("Failed to bind UDP: %v", err)
	}
	defer conn.Close()
	tuneUDPSocket(conn)

	cleanup, err := windev.ConfigureRouting(luid, cfg)
	if err != nil {
		log.Fatalf("Failed to configure routing: %v", err)
	}
	defer cleanup()

	eng, err := engine.New(cfg, tunDev, conn)
	if err != nil {
		log.Fatalf("Failed to build engine: %v", err)
	}
	errChan := make(chan error, 2)
	eng.Run(errChan)
	log.Printf("VEIL client running.")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("Received signal %v, shutting down...", sig)
	case err := <-errChan:
		log.Printf("Fatal error: %v", err)
	}
}

// bindUDP opens the tunnel socket. With a single configured peer the socket is
// connected to its endpoint, which puts every send/receive on the kernel's
// connected-UDP fast path (one route/WFP classification per flow instead of
// per packet) — this is where Windows loses most upload throughput otherwise.
func bindUDP(cfg *config.Config) (*net.UDPConn, error) {
	local := &net.UDPAddr{Port: cfg.Interface.ListenPort}
	if len(cfg.Peers) == 1 && cfg.Peers[0].Endpoint != "" {
		remote, err := net.ResolveUDPAddr("udp", cfg.Peers[0].Endpoint)
		if err == nil {
			if conn, err := net.DialUDP("udp", local, remote); err == nil {
				log.Printf("UDP socket connected to %s", remote)
				return conn, nil
			}
			log.Printf("Warning: connected UDP bind failed, falling back to unconnected socket")
		}
	}
	return net.ListenUDP("udp", local)
}

// tuneUDPSocket applies the Windows socket tuning the data plane depends on:
// large buffers so bursts survive scheduling gaps (the Winsock defaults are
// tiny and show up as packet loss under load), and SIO_UDP_CONNRESET off so a
// stray ICMP port-unreachable does not surface as WSAECONNRESET on later
// receives.
func tuneUDPSocket(conn *net.UDPConn) {
	if err := conn.SetReadBuffer(8 << 20); err != nil {
		log.Printf("Warning: failed to set UDP read buffer: %v", err)
	}
	if err := conn.SetWriteBuffer(8 << 20); err != nil {
		log.Printf("Warning: failed to set UDP write buffer: %v", err)
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		log.Printf("Warning: raw socket access unavailable: %v", err)
		return
	}
	if err := raw.Control(func(fd uintptr) {
		// SIO_UDP_CONNRESET = _WSAIOW(IOC_VENDOR, 12)
		const sioUDPConnReset = windows.IOC_IN | windows.IOC_VENDOR | 12
		flag := uint32(0) // FALSE: do not report ICMP-unreachable as WSAECONNRESET
		var returned uint32
		if err := windows.WSAIoctl(windows.Handle(fd), sioUDPConnReset,
			(*byte)(unsafe.Pointer(&flag)), uint32(unsafe.Sizeof(flag)),
			nil, 0, &returned, nil, 0); err != nil {
			log.Printf("Warning: failed to disable UDP connreset: %v", err)
		}
	}); err != nil {
		log.Printf("Warning: socket control failed: %v", err)
	}
}
