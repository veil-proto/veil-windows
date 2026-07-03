//go:build windows

package veiltun

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Ring capacity bounds and the maximum L3 packet size, from the driver ABI.
const (
	RingCapacityMin = 0x20000   // 128 KiB
	RingCapacityMax = 0x4000000 // 64 MiB
	MaxPacketSize   = 0xFFFF    // WINTUN_MAX_IP_PACKET_SIZE
)

// Session is an active send/receive session on an Adapter. The driver exposes
// two lock-free rings behind it; ReceivePacket/AllocateSendPacket hand back
// pointers directly into those rings, which is why callers must Release/Send
// promptly and must not retain the returned slices.
type Session struct {
	handle uintptr
}

// StartSession opens a session with the given ring capacity (a power of two
// between RingCapacityMin and RingCapacityMax).
func (a *Adapter) StartSession(capacity uint32) (*Session, error) {
	r0, _, e1 := syscall.SyscallN(procs.startSession, a.handle, uintptr(capacity))
	if r0 == 0 {
		return nil, e1
	}
	return &Session{handle: r0}, nil
}

// End tears down the session. After End the handle must not be used again.
func (s *Session) End() {
	if s.handle == 0 {
		return
	}
	syscall.SyscallN(procs.endSession, s.handle)
	s.handle = 0
}

// ReadWaitEvent returns the driver-managed event that becomes signaled when at
// least one packet is available to receive. Do not CloseHandle it.
func (s *Session) ReadWaitEvent() windows.Handle {
	r0, _, _ := syscall.SyscallN(procs.getReadWait, s.handle)
	return windows.Handle(r0)
}

// ReceivePacket returns the next inbound packet as a slice aliasing the receive
// ring. On success the caller must copy what it needs and then call
// ReleaseReceivePacket. When no packet is queued it returns
// windows.ERROR_NO_MORE_ITEMS.
func (s *Session) ReceivePacket() ([]byte, error) {
	var size uint32
	r0, _, e1 := syscall.SyscallN(procs.receivePacket, s.handle, uintptr(unsafe.Pointer(&size)))
	if r0 == 0 {
		return nil, e1
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(r0)), size), nil
}

// ReleaseReceivePacket returns a packet from ReceivePacket to the ring.
func (s *Session) ReleaseReceivePacket(packet []byte) {
	syscall.SyscallN(procs.releasePacket, s.handle, uintptr(unsafe.Pointer(&packet[0])))
}

// AllocateSendPacket reserves size bytes in the send ring and returns a slice to
// fill with an L3 packet. When the ring is full it returns
// windows.ERROR_BUFFER_OVERFLOW. Allocation order defines send order.
func (s *Session) AllocateSendPacket(size int) ([]byte, error) {
	r0, _, e1 := syscall.SyscallN(procs.allocatePacket, s.handle, uintptr(size))
	if r0 == 0 {
		return nil, e1
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(r0)), size), nil
}

// SendPacket commits a packet obtained from AllocateSendPacket to the wire.
func (s *Session) SendPacket(packet []byte) {
	syscall.SyscallN(procs.sendPacket, s.handle, uintptr(unsafe.Pointer(&packet[0])))
}
