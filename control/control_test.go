package control

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
)

// fakeHandler is an in-memory Handler that records calls and flips state.
type fakeHandler struct {
	mu         sync.Mutex
	state      State
	name       string
	lastConfig string
	logs       *LogBuffer
}

func (h *fakeHandler) Connect(cfg, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastConfig = cfg
	h.name = name
	h.state = StateConnected
	return nil
}

func (h *fakeHandler) Disconnect() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state = StateDisconnected
	h.name = ""
	return nil
}

func (h *fakeHandler) Status() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == "" {
		h.state = StateDisconnected
	}
	return Status{State: h.state, Name: h.name}
}

func (h *fakeHandler) Logs(since uint64) []LogLine {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.logs == nil {
		return nil
	}
	return h.logs.Since(since)
}

// ParseConfig/SerializeConfig aren't exercised by these transport-level
// tests (real parsing is covered where the sidecar's Handler implementation
// delegates to veil/config); a fake round-trip through a single opaque
// string is enough to satisfy the Handler interface here.
func (h *fakeHandler) ParseConfig(configText string) (ParsedConfig, error) {
	return ParsedConfig{Interface: ParsedInterface{PrivateKey: configText}}, nil
}

func (h *fakeHandler) SerializeConfig(cfg ParsedConfig) (string, error) {
	return cfg.Interface.PrivateKey, nil
}

func TestControlRoundTrip(t *testing.T) {
	h := &fakeHandler{}
	srv := &Server{Handler: h}

	c1, c2 := net.Pipe()
	go srv.serveConn(c1)
	client := NewClient(c2)
	defer client.Close()

	// Initial status: disconnected.
	resp, err := client.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !resp.OK || resp.Status == nil || resp.Status.State != StateDisconnected {
		t.Fatalf("initial status = %+v", resp)
	}

	// Connect.
	resp, err = client.Connect("cfg-text", "Home")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !resp.OK || resp.Status.State != StateConnected || resp.Status.Name != "Home" {
		t.Fatalf("connect status = %+v", resp)
	}
	if h.lastConfig != "cfg-text" {
		t.Errorf("handler got config %q, want cfg-text", h.lastConfig)
	}

	// Reuse the same connection to poll status again.
	resp, _ = client.Status()
	if resp.Status.State != StateConnected {
		t.Errorf("status after connect = %v", resp.Status.State)
	}

	// Disconnect.
	resp, err = client.Disconnect()
	if err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if resp.Status.State != StateDisconnected {
		t.Errorf("disconnect status = %v", resp.Status.State)
	}
}

func TestControlLogsRoundTrip(t *testing.T) {
	h := &fakeHandler{logs: NewLogBuffer(10)}
	h.logs.Append("first line")
	h.logs.Append("second line")

	srv := &Server{Handler: h}
	c1, c2 := net.Pipe()
	go srv.serveConn(c1)
	client := NewClient(c2)
	defer client.Close()

	logs, err := client.Logs(0)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("logs = %+v, want 2 lines", logs)
	}
	if logs[0].Msg != "first line" || logs[1].Msg != "second line" {
		t.Fatalf("unexpected log content: %+v", logs)
	}

	// A third line arrives; polling since the last-seen Seq should only
	// return the new one.
	h.logs.Append("third line")
	more, err := client.Logs(logs[len(logs)-1].Seq)
	if err != nil {
		t.Fatalf("logs since: %v", err)
	}
	if len(more) != 1 || more[0].Msg != "third line" {
		t.Fatalf("logs since = %+v, want just 'third line'", more)
	}
}

func TestParseAndSerializeConfigRoundTrip(t *testing.T) {
	srv := &Server{Handler: &fakeHandler{}}
	c1, c2 := net.Pipe()
	go srv.serveConn(c1)
	client := NewClient(c2)
	defer client.Close()

	resp, err := client.Do(Request{Cmd: CmdParseConfig, Config: "deadbeef"})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if !resp.OK || resp.ParsedConfig == nil || resp.ParsedConfig.Interface.PrivateKey != "deadbeef" {
		t.Fatalf("parseConfig response = %+v", resp)
	}

	resp, err = client.Do(Request{Cmd: CmdSerializeConfig, ParsedConfig: resp.ParsedConfig})
	if err != nil {
		t.Fatalf("serializeConfig: %v", err)
	}
	if !resp.OK || resp.Config != "deadbeef" {
		t.Fatalf("serializeConfig response = %+v", resp)
	}
}

func TestSerializeConfigRequiresParsedConfig(t *testing.T) {
	srv := &Server{Handler: &fakeHandler{}}
	c1, c2 := net.Pipe()
	go srv.serveConn(c1)
	client := NewClient(c2)
	defer client.Close()

	resp, err := client.Do(Request{Cmd: CmdSerializeConfig})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Errorf("expected error response for missing parsedConfig, got %+v", resp)
	}
}

func TestUnknownCommand(t *testing.T) {
	srv := &Server{Handler: &fakeHandler{}}
	c1, c2 := net.Pipe()
	go srv.serveConn(c1)
	client := NewClient(c2)
	defer client.Close()

	resp, err := client.Do(Request{Cmd: "bogus"})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Errorf("expected error response, got %+v", resp)
	}
}

// TestServeIO exercises the sidecar transport (any io.Reader/io.Writer pair,
// e.g. a child process's stdin/stdout) rather than the named-pipe-oriented
// net.Conn path serveConn/Serve use. Wires two io.Pipes to stand in for a
// parent process's write-end-of-child's-stdin and read-end-of-child's-stdout,
// and drives the same Request/Response wire format directly since Client
// itself expects a net.Conn.
func TestServeIO(t *testing.T) {
	h := &fakeHandler{}
	srv := &Server{Handler: h}

	// reqR/reqW: test writes requests, ServeIO reads them (stands in for the
	// sidecar's stdin). respR/respW: ServeIO writes responses, test reads
	// them (stands in for the sidecar's stdout).
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()
	go srv.ServeIO(reqR, respW)

	respBr := &lineReader{r: respR}

	send := func(req Request) Response {
		b, err := jsonMarshalLine(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := reqW.Write(b); err != nil {
			t.Fatalf("write request: %v", err)
		}
		var resp Response
		if err := respBr.readJSON(&resp); err != nil {
			t.Fatalf("read response: %v", err)
		}
		return resp
	}

	resp := send(Request{Cmd: CmdStatus})
	if !resp.OK || resp.Status == nil || resp.Status.State != StateDisconnected {
		t.Fatalf("initial status over ServeIO = %+v", resp)
	}

	resp = send(Request{Cmd: CmdConnect, Config: "cfg-text", Name: "Home"})
	if !resp.OK || resp.Status.State != StateConnected || resp.Status.Name != "Home" {
		t.Fatalf("connect over ServeIO = %+v", resp)
	}
	if h.lastConfig != "cfg-text" {
		t.Errorf("handler got config %q, want cfg-text", h.lastConfig)
	}

	resp = send(Request{Cmd: CmdDisconnect})
	if !resp.OK || resp.Status.State != StateDisconnected {
		t.Fatalf("disconnect over ServeIO = %+v", resp)
	}
}

func jsonMarshalLine(req Request) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeMessage(&buf, req); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// lineReader is a minimal newline-delimited-JSON reader for the test above,
// independent of bufio.Reader's own buffering so it plays nicely with
// io.Pipe's synchronous, unbuffered semantics.
type lineReader struct {
	r   io.Reader
	buf []byte
}

func (lr *lineReader) readJSON(v any) error {
	for {
		if i := bytes.IndexByte(lr.buf, '\n'); i >= 0 {
			line := lr.buf[:i]
			lr.buf = lr.buf[i+1:]
			return json.Unmarshal(line, v)
		}
		chunk := make([]byte, 4096)
		n, err := lr.r.Read(chunk)
		if n > 0 {
			lr.buf = append(lr.buf, chunk[:n]...)
		}
		if err != nil {
			return err
		}
	}
}
