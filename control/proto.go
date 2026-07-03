// Package control is the local control channel between the VEIL tunnel service
// (veil-service) and its front-ends (the tray GUI, a CLI). The wire protocol is
// newline-delimited JSON request/response over a Windows named pipe; the
// protocol and the server/client dispatch are platform-neutral (they operate on
// a net.Conn), and only the pipe transport itself (pipe_windows.go) is
// OS-specific.
package control

import (
	"bufio"
	"encoding/json"
	"io"
)

// PipeName is the named pipe the service listens on.
const PipeName = `\\.\pipe\veil-service`

// Command names.
const (
	CmdStatus     = "status"
	CmdConnect    = "connect"
	CmdDisconnect = "disconnect"
	CmdLogs       = "logs"
)

// Request is one command from a front-end to the service.
type Request struct {
	Cmd    string `json:"cmd"`
	Config string `json:"config,omitempty"` // config text, for connect
	Name   string `json:"name,omitempty"`   // display name, for connect
	Since  uint64 `json:"since,omitempty"`  // log cursor (exclusive), for logs
}

// Response is the service's reply. Status is populated on every successful reply
// so a front-end always gets the current state back.
type Response struct {
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
	Status *Status   `json:"status,omitempty"`
	Logs   []LogLine `json:"logs,omitempty"` // populated on a logs request
}

// LogLine is one captured line of service log output. Seq is a monotonic,
// 1-based cursor (not a timestamp) assigned by the ring buffer in sequence
// order, so a front-end can ask for "everything after Seq N" without
// worrying about clock skew or duplicate timestamps within the same second.
// Since=0 always means "from the beginning of the retained backlog."
type LogLine struct {
	Seq   uint64 `json:"seq"`
	Time  int64  `json:"time"` // unix seconds when the line was captured
	Level string `json:"level,omitempty"`
	Msg   string `json:"msg"`
}

// State is the coarse tunnel state a front-end renders.
type State string

const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
)

// Status is a snapshot of the tunnel for display.
type Status struct {
	State State        `json:"state"`
	Name  string       `json:"name,omitempty"`  // active config's display name
	Iface string       `json:"iface,omitempty"` // TUN interface name
	Peers []PeerStatus `json:"peers,omitempty"`
}

// PeerStatus mirrors engine.PeerStats in a JSON/display-friendly shape (times as
// Unix seconds). The service maps engine stats into this.
type PeerStatus struct {
	PublicKey     string `json:"public_key"`
	Endpoint      string `json:"endpoint,omitempty"`
	Connected     bool   `json:"connected"`
	LastHandshake int64  `json:"last_handshake,omitempty"` // unix seconds, 0 if none
	LastReceived  int64  `json:"last_received,omitempty"`
	RxBytes       uint64 `json:"rx_bytes"`
	TxBytes       uint64 `json:"tx_bytes"`
	FrameBudget   int    `json:"frame_budget"`
}

// writeMessage marshals v as one newline-terminated JSON line.
func writeMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// readMessage reads one newline-terminated JSON line into v.
func readMessage(r *bufio.Reader, v any) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}
