package control

import (
	"bufio"
	"io"
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
	// Logs returns every captured log line with Seq > since, oldest first.
	// Implementations are expected to back this with a LogBuffer (or
	// equivalent) that spans tunnel restarts, not just the current session.
	Logs(since uint64) []LogLine
	// ParseConfig parses .conf text into a structured, JSON-friendly form for
	// a front-end's split-tunnel editor. This package has no config-parsing
	// logic of its own — implementations are expected to delegate to
	// github.com/veil-proto/veil/config, keeping that logic in one place
	// rather than duplicated in a frontend.
	ParseConfig(configText string) (ParsedConfig, error)
	// SerializeConfig renders a structured config back into .conf text, the
	// inverse of ParseConfig.
	SerializeConfig(cfg ParsedConfig) (string, error)
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
	s.ServeIO(conn, conn)
}

// ServeIO runs the same request/response loop as serveConn, but over any
// io.Reader/io.Writer pair instead of a net.Conn — in particular, a sidecar
// process's os.Stdin/os.Stdout, so a front-end can drive this Server without
// a named pipe or any other network listener at all. Returns once r returns
// an error (EOF on the peer closing its end, typically).
func (s *Server) ServeIO(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	for {
		var req Request
		if err := readMessage(br, &req); err != nil {
			return
		}
		if err := writeMessage(w, s.dispatch(req)); err != nil {
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
	case CmdLogs:
		logs := s.Handler.Logs(req.Since)
		st := s.Handler.Status()
		return Response{OK: true, Status: &st, Logs: logs}
	case CmdParseConfig:
		pc, err := s.Handler.ParseConfig(req.Config)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, ParsedConfig: &pc}
	case CmdSerializeConfig:
		if req.ParsedConfig == nil {
			return Response{Error: "serializeConfig: missing parsedConfig"}
		}
		text, err := s.Handler.SerializeConfig(*req.ParsedConfig)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Config: text}
	default:
		return Response{Error: "unknown command: " + req.Cmd}
	}
	st := s.Handler.Status()
	return Response{OK: true, Status: &st}
}
