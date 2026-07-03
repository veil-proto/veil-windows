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

// Close closes the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }
