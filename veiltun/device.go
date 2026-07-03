//go:build windows

package veiltun

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// batchSize is how many packets one Read/Write call moves. It matches the
// engine's UDP batch sizing so the two hot loops stay balanced.
const batchSize = 128

// ringCapacity is the send/receive ring size. 8 MiB absorbs bursts that would
// otherwise show up as drops under load (the driver drops on a full ring).
const ringCapacity = 0x800000

// spinBudget is how long Read busy-polls the receive ring before falling back
// to blocking on the read-wait event. The blocking wait (WaitForSingleObject)
// has a wakeup latency that can reach into the low milliseconds on Windows; at
// ~1.4 KB/packet that alone would cap a single flow to a few Mbps. Spinning for
// a few hundred microseconds first lets steady traffic be picked up without
// ever paying that latency, while an idle tunnel still parks on the event after
// one short spin.
const spinBudget = 500 * time.Microsecond

// canSpin disables the busy-poll on single-core machines, where it would
// compete with the very thread feeding the ring.
var canSpin = runtime.NumCPU() > 1

// NativeTun is a batched TUN device backed by the VEIL driver. It implements the
// engine's Tun interface (ReadBatch/WriteBatch/BatchSize/Name/Close) plus LUID.
//
// Read and Write each assume a single caller goroutine (the engine's two hot
// loops), matching the driver's ring model; there is no internal locking on the
// data path.
type NativeTun struct {
	adapter  *Adapter
	session  *Session
	name     string
	mtu      int
	readWait windows.Handle

	running   sync.WaitGroup
	closeOnce sync.Once
	closed    atomic.Bool
}

// CreateTUN creates a VEIL TUN adapter with the given name and starts a session
// on it. mtu is retained for reporting; the effective interface MTU is set
// separately via winipcfg by the front-end.
func CreateTUN(name string, mtu int) (*NativeTun, error) {
	adapter, err := CreateAdapter(name, nil)
	if err != nil {
		return nil, err
	}
	session, err := adapter.StartSession(ringCapacity)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("veiltun: start session: %w", err)
	}
	if mtu <= 0 {
		mtu = 1420
	}
	return &NativeTun{
		adapter:  adapter,
		session:  session,
		name:     name,
		mtu:      mtu,
		readWait: session.ReadWaitEvent(),
	}, nil
}

// Name returns the adapter name.
func (t *NativeTun) Name() string { return t.name }

// MTU returns the MTU CreateTUN was given.
func (t *NativeTun) MTU() int { return t.mtu }

// BatchSize reports the maximum packets a Read/Write call handles.
func (t *NativeTun) BatchSize() int { return batchSize }

// LUID returns the adapter's Windows interface LUID.
func (t *NativeTun) LUID() uint64 {
	if t.closed.Load() {
		return 0
	}
	return t.adapter.LUID()
}

// Read fills up to len(bufs) packets, storing packet i at bufs[i][offset:] and
// its length at sizes[i], and returns how many it read. It blocks until at
// least one packet is available (after a short busy-poll) or the device closes.
func (t *NativeTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.running.Add(1)
	defer t.running.Done()

retry:
	if t.closed.Load() {
		return 0, os.ErrClosed
	}

	spinning := false
	var spinDeadline time.Time
	n := 0
	for i := 0; i < len(bufs); i++ {
		if t.closed.Load() {
			if n > 0 {
				return n, nil
			}
			return 0, os.ErrClosed
		}
		packet, err := t.session.ReceivePacket()
		switch err {
		case nil:
			sizes[i] = copy(bufs[i][offset:], packet)
			t.session.ReleaseReceivePacket(packet)
			n++
		case windows.ERROR_NO_MORE_ITEMS:
			if n > 0 {
				// Deliver what we have rather than stalling the batch.
				return n, nil
			}
			if !canSpin {
				windows.WaitForSingleObject(t.readWait, windows.INFINITE)
				goto retry
			}
			if !spinning {
				spinning = true
				spinDeadline = time.Now().Add(spinBudget)
			} else if time.Now().After(spinDeadline) {
				windows.WaitForSingleObject(t.readWait, windows.INFINITE)
				goto retry
			}
			runtime.Gosched()
			i-- // re-poll this slot
			continue
		case windows.ERROR_HANDLE_EOF:
			if n > 0 {
				return n, nil
			}
			return 0, os.ErrClosed
		default:
			if n > 0 {
				return n, nil
			}
			return 0, fmt.Errorf("veiltun: read: %w", err)
		}
	}
	return n, nil
}

// Write sends each of bufs (packet i is bufs[i][offset:]). Packets that don't
// fit the ring are dropped (the sender will retransmit); a terminating adapter
// returns os.ErrClosed.
func (t *NativeTun) Write(bufs [][]byte, offset int) (int, error) {
	t.running.Add(1)
	defer t.running.Done()
	if t.closed.Load() {
		return 0, os.ErrClosed
	}

	for i, buf := range bufs {
		size := len(buf) - offset
		if size <= 0 || size > MaxPacketSize {
			continue
		}
		packet, err := t.session.AllocateSendPacket(size)
		switch err {
		case nil:
			copy(packet, buf[offset:])
			t.session.SendPacket(packet)
		case windows.ERROR_BUFFER_OVERFLOW:
			continue // ring full: drop, let the transport retransmit
		case windows.ERROR_HANDLE_EOF:
			return i, os.ErrClosed
		default:
			return i, fmt.Errorf("veiltun: write: %w", err)
		}
	}
	return len(bufs), nil
}

// Close ends the session and removes the adapter. It waits for any in-flight
// Read/Write to return first.
func (t *NativeTun) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		// Wake a Read blocked on the event so running.Wait can complete.
		windows.SetEvent(t.readWait)
		t.running.Wait()
		t.session.End()
		t.adapter.Close()
	})
	return nil
}
