package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

// Client is a JSON-RPC client on one connection. It is safe for concurrent
// use: calls may overlap, and their responses are matched by ID.
type Client struct {
	nc    net.Conn
	notes chan Notification
	done  chan struct{}

	closeOnce sync.Once
	closing   chan struct{}

	wmu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[string]chan *Response
	err     error // why the connection ended
}

// ErrClosed is returned by calls on a client whose connection has ended.
var ErrClosed = errors.New("api: connection closed")

// Dial connects to the daemon's Unix socket at path.
func Dial(ctx context.Context, path string) (*Client, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return NewClient(nc), nil
}

// NewClient runs a client on an open connection.
func NewClient(nc net.Conn) *Client {
	c := &Client{nc: nc, notes: make(chan Notification, 64), done: make(chan struct{}), closing: make(chan struct{}), pending: map[string]chan *Response{}}
	go c.read()
	return c
}

// Notifications returns the channel server notifications (events) arrive on.
// It is closed when the connection ends. Read it whenever you subscribe to
// events: while it is full the client stops reading, and responses wait too.
func (c *Client) Notifications() <-chan Notification { return c.notes }

// Done is closed when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close closes the connection. Pending calls fail with ErrClosed.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closing) })
	err := c.nc.Close()
	<-c.done
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Call invokes method with params (nil for none) and decodes the result into
// result (nil to discard it). A server error comes back as an *Error.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	req := Request{JSONRPC: Version, Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("api: encode params: %w", err)
		}
		req.Params = p
	}
	ch := make(chan *Response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := strconv.FormatUint(c.nextID, 10)
	c.pending[id] = ch
	c.mu.Unlock()
	req.ID = json.RawMessage(id)

	if err := c.send(req); err != nil {
		c.forget(id)
		return err
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.err
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("api: decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

// Notify sends a notification: a request with no ID and no response.
func (c *Client) Notify(method string, params any) error {
	req := Request{JSONRPC: Version, Method: method}
	if params != nil {
		p, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = p
	}
	return c.send(req)
}

func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.nc.Write(b); err != nil {
		return fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return nil
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) read() {
	err := ErrClosed
	defer func() {
		_ = c.nc.Close()
		c.mu.Lock()
		c.err = err
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
		close(c.notes)
		close(c.done)
	}()
	r := bufio.NewReaderSize(c.nc, 4096)
	for {
		line, rerr := readLine(r, MaxMessage)
		if rerr != nil && len(line) == 0 {
			return
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			err = fmt.Errorf("%w: unreadable message from server", ErrClosed)
			return
		}
		if m.Method != "" && len(m.ID) == 0 {
			select {
			case c.notes <- Notification{Method: m.Method, Params: m.Params}:
			case <-c.closing:
				return
			}
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ok {
			ch <- &Response{JSONRPC: m.JSONRPC, ID: m.ID, Result: m.Result, Error: m.Error}
		} else if m.Error != nil && string(m.ID) == "null" {
			// The server could not tell which request this answers (a parse
			// error, say); nothing can be matched, so the connection is done.
			err = fmt.Errorf("%w: %w", ErrClosed, m.Error)
			return
		}
		if rerr != nil {
			return
		}
	}
}
