//go:build windows

package windev

import (
	"fmt"

	"github.com/veil-proto/veil-windows/veiltun"
	"github.com/veil-proto/veil/engine"
)

// TunDevice is the Windows TUN device, backed by VEIL's own native driver
// binding (the veiltun package). Unlike the non-Windows build it carries no
// dependency on the vendored WireGuard tun/wintun packages.
type TunDevice struct {
	dev  *veiltun.NativeTun
	name string
}

func NewTunDevice(name string) (*TunDevice, error) {
	if name == "" {
		name = "veil0"
	}
	dev, err := veiltun.CreateTUN(name, engine.DefaultTunMTU)
	if err != nil {
		return nil, fmt.Errorf("failed to create VEIL tun adapter: %w", err)
	}
	return &TunDevice{dev: dev, name: name}, nil
}

// BatchSize reports how many packets a single ReadBatch/WriteBatch may carry.
func (t *TunDevice) BatchSize() int {
	return t.dev.BatchSize()
}

// ReadBatch reads up to len(bufs) packets. Packet i is stored in
// bufs[i][offset:] and its length in sizes[i]. It returns the packet count.
func (t *TunDevice) ReadBatch(bufs [][]byte, sizes []int, offset int) (int, error) {
	return t.dev.Read(bufs, sizes, offset)
}

// WriteBatch writes len(bufs) packets; packet i is bufs[i][offset:].
func (t *TunDevice) WriteBatch(bufs [][]byte, offset int) (int, error) {
	return t.dev.Write(bufs, offset)
}

func (t *TunDevice) Close() error {
	return t.dev.Close()
}

func (t *TunDevice) Name() string {
	return t.name
}

// LUID returns the adapter's Windows interface LUID, used by the front-end to
// configure addressing/routes/DNS via winipcfg.
func (t *TunDevice) LUID() uint64 {
	return t.dev.LUID()
}
