package control

import (
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
