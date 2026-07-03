package control

import (
	"bufio"
	"net"
)

// Handler is the tunnel-control surface the service implements. Methods may be
// called concurrently from multiple client connections, so implementations must
// be safe for concurrent use.
type Handler interface {
	// Connect brings up (or switches to) the given config. name is the display
	// name to report back in Status.
	Connect(configText, name string) error
	// Disconnect tears the tunnel down. It is not an error to call it while
	// already disconnected.
	Disconnect() error
	// Status returns the current tunnel snapshot.
	Status() Status
}

// Server dispatches control requests to a Handler.
type Server struct {
	Handler Handler
}

// Serve accepts connections on l until it errors (e.g. the listener is closed).
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.serveConn(conn)
	}
}

// serveConn handles requests on one connection until the peer disconnects. A
// front-end may keep the connection open and poll status on it repeatedly.
func (s *Server) serveConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		var req Request
		if err := readMessage(r, &req); err != nil {
			return
		}
		if err := writeMessage(conn, s.dispatch(req)); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(req Request) Response {
	switch req.Cmd {
	case CmdStatus:
		st := s.Handler.Status()
		return Response{OK: true, Status: &st}
	case CmdConnect:
		if err := s.Handler.Connect(req.Config, req.Name); err != nil {
			return Response{Error: err.Error()}
		}
	case CmdDisconnect:
		if err := s.Handler.Disconnect(); err != nil {
			return Response{Error: err.Error()}
		}
	default:
		return Response{Error: "unknown command: " + req.Cmd}
	}
	st := s.Handler.Status()
	return Response{OK: true, Status: &st}
}
