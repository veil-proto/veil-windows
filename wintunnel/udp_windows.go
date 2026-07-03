//go:build windows

package wintunnel

import (
	"log"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/veil-proto/veil/config"
)

// bindUDP opens the tunnel socket. With a single configured peer the socket is
// connected to its endpoint, which puts every send/receive on the kernel's
// connected-UDP fast path (one route/WFP classification per flow instead of per
// packet) — where Windows otherwise loses most upload throughput.
func bindUDP(cfg *config.Config) (*net.UDPConn, error) {
	local := &net.UDPAddr{Port: cfg.Interface.ListenPort}
	if len(cfg.Peers) == 1 && cfg.Peers[0].Endpoint != "" {
		if remote, err := net.ResolveUDPAddr("udp", cfg.Peers[0].Endpoint); err == nil {
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
// large buffers so bursts survive scheduling gaps, and SIO_UDP_CONNRESET off so
// a stray ICMP port-unreachable does not surface as WSAECONNRESET on later
// receives (which would otherwise kill the receive loop).
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
