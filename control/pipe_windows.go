//go:build windows

package control

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

// pipeSDDL restricts the control pipe: full control to the built-in
// Administrators group (BA) and SYSTEM (SY) — the service runs as one of these —
// plus read/write for the interactive logon group (IU) so the logged-in user's
// tray can send commands without elevation. Bringing the tunnel up/down is
// therefore available to whoever is physically logged in, which is the intended
// single-user-desktop model; tighten IU to a dedicated group if that ever needs
// to be more restrictive.
const pipeSDDL = "D:P(A;;GA;;;BA)(A;;GA;;;SY)(A;;GRGW;;;IU)"

// Listen creates the control named pipe with the access policy above.
func Listen() (net.Listener, error) {
	return winio.ListenPipe(PipeName, &winio.PipeConfig{SecurityDescriptor: pipeSDDL})
}

// Dial connects to the service's control pipe, waiting up to timeout for it to
// exist (the service may still be starting).
func Dial(timeout time.Duration) (net.Conn, error) {
	return winio.DialPipe(PipeName, &timeout)
}
