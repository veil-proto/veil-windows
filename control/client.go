package control

import (
	"bufio"
	"net"
	"sync"
)

// Client is a front-end's handle to the control channel. One Client wraps one
// connection and serializes requests on it, so a tray can reuse a single Client
// to poll status repeatedly.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
}

// NewClient wraps an established connection (from Dial on Windows, or net.Pipe
// in tests).
func NewClient(conn net.Conn) *Client {
	return &Client{conn: conn, r: bufio.NewReader(conn)}
}

// Do sends one request and returns the response.
func (c *Client) Do(req Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := writeMessage(c.conn, req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := readMessage(c.r, &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// Status queries the current tunnel state.
func (c *Client) Status() (Response, error) { return c.Do(Request{Cmd: CmdStatus}) }

// Connect brings up the given config under the given display name.
func (c *Client) Connect(configText, name string) (Response, error) {
	return c.Do(Request{Cmd: CmdConnect, Config: configText, Name: name})
}

// Disconnect tears the tunnel down.
func (c *Client) Disconnect() (Response, error) { return c.Do(Request{Cmd: CmdDisconnect}) }

// Logs fetches every log line captured since the given cursor (0 for the full
// retained backlog). Callers should track the highest Seq they've seen and
// pass it back in as since on the next call to only receive new lines.
func (c *Client) Logs(since uint64) ([]LogLine, error) {
	resp, err := c.Do(Request{Cmd: CmdLogs, Since: since})
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, &responseError{msg: resp.Error}
	}
	return resp.Logs, nil
}

// responseError wraps a service-reported error string as an error value.
type responseError struct{ msg string }

func (e *responseError) Error() string { return e.msg }

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }
