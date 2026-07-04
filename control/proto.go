// Package control is the local control channel between the VEIL tunnel
// backend (the veil-sidecar process the Tauri shell spawns) and its
// front-end. The wire protocol is newline-delimited JSON request/response;
// ServeIO runs it over any io.Reader/io.Writer pair (in particular a child
// process's stdin/stdout), so the protocol and dispatch have no transport
// dependency at all — there is deliberately no named pipe or other OS-specific
// listener in this package.
package control

import (
	"bufio"
	"encoding/json"
	"io"
)

// Command names.
const (
	CmdStatus          = "status"
	CmdConnect         = "connect"
	CmdDisconnect      = "disconnect"
	CmdLogs            = "logs"
	CmdParseConfig     = "parseConfig"     // config text -> structured ParsedConfig, for the split-tunnel editor
	CmdSerializeConfig = "serializeConfig" // structured ParsedConfig -> config text, inverse of parseConfig
)

// Request is one command from a front-end to the backend.
type Request struct {
	Cmd          string        `json:"cmd"`
	Config       string        `json:"config,omitempty"`       // config text, for connect/parseConfig
	Name         string        `json:"name,omitempty"`         // display name, for connect
	Since        uint64        `json:"since,omitempty"`        // log cursor (exclusive), for logs
	ParsedConfig *ParsedConfig `json:"parsedConfig,omitempty"` // for serializeConfig
}

// Response is the backend's reply. Status is populated on every successful
// reply so a front-end always gets the current state back.
type Response struct {
	OK           bool          `json:"ok"`
	Error        string        `json:"error,omitempty"`
	Status       *Status       `json:"status,omitempty"`
	Logs         []LogLine     `json:"logs,omitempty"`         // populated on a logs request
	ParsedConfig *ParsedConfig `json:"parsedConfig,omitempty"` // populated on a parseConfig request
	Config       string        `json:"config,omitempty"`       // populated on a serializeConfig request
}

// ParsedConfig is a JSON-friendly view of github.com/veil-proto/veil/config's
// Config, for the structured split-tunnel editor. Byte fields (keys, secrets)
// are hex strings, matching how the .conf format itself represents them, so
// round-tripping through ParseConfig/SerializeConfig never has to touch raw
// key material as anything but the same hex text already on disk.
type ParsedConfig struct {
	Interface ParsedInterface `json:"interface"`
	Peers     []ParsedPeer    `json:"peers"`
}

// ParsedInterface mirrors config.InterfaceConfig.
type ParsedInterface struct {
	PrivateKey             string `json:"privateKey"` // hex
	Address                string `json:"address,omitempty"`
	BindAddress            string `json:"bindAddress,omitempty"`
	ListenPort             int    `json:"listenPort,omitempty"`
	NID                    string `json:"nid"`                 // hex
	NetSecret              string `json:"netSecret,omitempty"` // hex, empty when NetSecretInsecure
	NetSecretInsecure      bool   `json:"netSecretInsecure,omitempty"`
	AllowInsecureNetSecret bool   `json:"allowInsecureNetSecret,omitempty"`
	Padding                string `json:"padding,omitempty"`
	DNS                    string `json:"dns,omitempty"`
	FwMark                 int    `json:"fwMark,omitempty"`
}

// ParsedPeer mirrors config.PeerConfig.
type ParsedPeer struct {
	PublicKey           string   `json:"publicKey"` // hex
	AllowedIPs          []string `json:"allowedIPs,omitempty"`
	Endpoint            string   `json:"endpoint,omitempty"`
	PersistentKeepalive int      `json:"persistentKeepalive,omitempty"`
	PresharedKey        string   `json:"presharedKey,omitempty"` // hex
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
