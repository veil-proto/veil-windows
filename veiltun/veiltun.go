//go:build windows

// Package veiltun is VEIL's own native integration with the userspace TUN
// driver on Windows. It binds the driver's documented C ABI (the prototypes in
// wintun.h) directly via LoadLibraryEx/GetProcAddress, so the Windows client no
// longer depends on the vendored WireGuard Go bindings (the veil-go/wintun
// package). Only the published C ABI is shared with upstream; this Go code is
// VEIL's own.
//
// Security note: the driver DLL is resolved only from the application directory
// and System32, never the current working directory, so a stray veiltun.dll
// dropped next to wherever the client happens to be launched cannot be loaded
// in preference to the real one.
package veiltun

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// driverDLL is the on-disk name of the VEIL TUN driver. It is deliberately
// VEIL-named (not the upstream filename) so nothing at the OS layer carries an
// identifying WireGuard filename.
const driverDLL = "veiltun.dll"

// TunnelType is the cosmetic adapter type reported to Windows.
const TunnelType = "VEIL"

// procTable holds the resolved addresses of every driver entry point we call.
type procTable struct {
	createAdapter  uintptr
	openAdapter    uintptr
	closeAdapter   uintptr
	deleteDriver   uintptr
	getAdapterLUID uintptr
	runningVersion uintptr
	startSession   uintptr
	endSession     uintptr
	getReadWait    uintptr
	receivePacket  uintptr
	releasePacket  uintptr
	allocatePacket uintptr
	sendPacket     uintptr
}

var (
	loadOnce sync.Once
	loadErr  error
	procs    procTable
)

// load resolves the driver DLL and its entry points exactly once.
func load() error {
	loadOnce.Do(func() {
		// LOAD_LIBRARY_SEARCH_APPLICATION_DIR | LOAD_LIBRARY_SEARCH_SYSTEM32:
		// restrict resolution to trusted directories, excluding the CWD.
		const safeSearchFlags = 0x00000200 | 0x00000800
		module, err := windows.LoadLibraryEx(driverDLL, 0, safeSearchFlags)
		if err != nil {
			loadErr = fmt.Errorf("veiltun: load %s: %w", driverDLL, err)
			return
		}
		resolve := func(name string, dst *uintptr) {
			if loadErr != nil {
				return
			}
			addr, err := windows.GetProcAddress(module, name)
			if err != nil {
				loadErr = fmt.Errorf("veiltun: resolve %s: %w", name, err)
				return
			}
			*dst = addr
		}
		resolve("WintunCreateAdapter", &procs.createAdapter)
		resolve("WintunOpenAdapter", &procs.openAdapter)
		resolve("WintunCloseAdapter", &procs.closeAdapter)
		resolve("WintunDeleteDriver", &procs.deleteDriver)
		resolve("WintunGetAdapterLUID", &procs.getAdapterLUID)
		resolve("WintunGetRunningDriverVersion", &procs.runningVersion)
		resolve("WintunStartSession", &procs.startSession)
		resolve("WintunEndSession", &procs.endSession)
		resolve("WintunGetReadWaitEvent", &procs.getReadWait)
		resolve("WintunReceivePacket", &procs.receivePacket)
		resolve("WintunReleaseReceivePacket", &procs.releasePacket)
		resolve("WintunAllocateSendPacket", &procs.allocatePacket)
		resolve("WintunSendPacket", &procs.sendPacket)
	})
	return loadErr
}

// Adapter is a live handle to a VEIL TUN network adapter.
type Adapter struct {
	handle uintptr
}

// CreateAdapter creates (or, if one with the same name exists, reuses) a TUN
// adapter. requestedGUID may be nil to let the system assign a random GUID.
func CreateAdapter(name string, requestedGUID *windows.GUID) (*Adapter, error) {
	if err := load(); err != nil {
		return nil, err
	}
	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	type16, err := windows.UTF16PtrFromString(TunnelType)
	if err != nil {
		return nil, err
	}
	r0, _, e1 := syscall.SyscallN(procs.createAdapter,
		uintptr(unsafe.Pointer(name16)),
		uintptr(unsafe.Pointer(type16)),
		uintptr(unsafe.Pointer(requestedGUID)))
	runtime.KeepAlive(name16)
	runtime.KeepAlive(type16)
	runtime.KeepAlive(requestedGUID)
	if r0 == 0 {
		return nil, fmt.Errorf("veiltun: create adapter %q: %w", name, e1)
	}
	return &Adapter{handle: r0}, nil
}

// Close releases the adapter and, if it was created (not merely opened), removes
// it from the system.
func (a *Adapter) Close() {
	if a.handle == 0 {
		return
	}
	syscall.SyscallN(procs.closeAdapter, a.handle)
	a.handle = 0
}

// LUID returns the Windows interface LUID of the adapter, used to configure its
// address/routes/DNS via the winipcfg API.
func (a *Adapter) LUID() uint64 {
	var luid uint64
	syscall.SyscallN(procs.getAdapterLUID, a.handle, uintptr(unsafe.Pointer(&luid)))
	return luid
}

// RunningVersion returns the version of the loaded driver, or an error if it is
// not loaded.
func RunningVersion() (uint32, error) {
	if err := load(); err != nil {
		return 0, err
	}
	r0, _, e1 := syscall.SyscallN(procs.runningVersion)
	if r0 == 0 {
		return 0, e1
	}
	return uint32(r0), nil
}
