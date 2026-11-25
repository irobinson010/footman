package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
)

// Handler runs one method. params is the raw "params" member (nil if absent).
// The result is marshalled as the response's "result"; a nil result becomes
// an empty object. Return an *Error to choose the error code; any other error
// is reported as CodeFailed. ctx ends when the connection closes or the
// server shuts down.
type Handler func(ctx context.Context, c *Conn, params json.RawMessage) (any, error)

// Server serves JSON-RPC 2.0 on any number of connections. Each message is one
// line of JSON (a request, a notification or a batch array). Requests on a
// connection are handled concurrently, so a slow `say.text` does not hold up
// `status.get`; responses carry the request ID and may come back in any order.
type Server struct {
	mu       sync.Mutex
	handlers map[string]Handler
	conns    map[*Conn]struct{}
	lis      []net.Listener
	closed   bool
	wg       sync.WaitGroup
}

// NewServer returns a server with no methods.
func NewServer() *Server {
	return &Server{handlers: map[string]Handler{}, conns: map[*Conn]struct{}{}}
}

// Handle registers h for method. Registering a method twice panics.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.handlers[method]; dup {
		panic("api: method " + method + " registered twice")
	}
	s.handlers[method] = h
}

// ErrServerClosed is returned by Serve after Close.
var ErrServerClosed = errors.New("api: server closed")

// Serve accepts connections on l until Close, and then returns
// ErrServerClosed.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
		return ErrServerClosed
	}
	s.lis = append(s.lis, l)
	s.mu.Unlock()
	for {
		nc, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return ErrServerClosed
			}
			return err
		}
		s.ServeConn(nc)
	}
}

// ServeConn serves one connection in the background until it closes, the
// peer sends something unreadable, or the server is closed.
func (s *Server) ServeConn(nc net.Conn) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{s: s, nc: nc, ctx: ctx, cancel: cancel, closers: map[uint64]func(){}}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		_ = nc.Close()
		return
	}
	s.conns[c] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		c.serve()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
}

// Close stops every listener, closes every connection, and waits for running
// handlers to return (their contexts are cancelled). It is safe to call more
// than once.
func (s *Server) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for _, l := range s.lis {
			_ = l.Close()
		}
		for c := range s.conns {
			c.close()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) handler(method string) Handler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handlers[method]
}

// Conn is one client connection. Handlers use it to push notifications and to
// tie resources (event subscriptions) to the connection's life.
type Conn struct {
	s      *Server
	nc     net.Conn
	ctx    context.Context
	cancel context.CancelFunc

	wmu sync.Mutex // serializes writes

	mu      sync.Mutex
	closed  bool
	nextID  uint64
	closers map[uint64]func()
	wg      sync.WaitGroup // handlers in flight
	bg      sync.WaitGroup // background work started with Go
}

// Go runs f in the background as part of the connection: once the
// connection has closed and its OnClose functions have run, the server
// waits for f to return. Call it only from a handler.
func (c *Conn) Go(f func()) { c.bg.Go(f) }

// Context returns a context that ends when the connection closes.
func (c *Conn) Context() context.Context { return c.ctx }

// Notify sends a notification to the client. It blocks while the client is
// not reading; it fails once the connection is closed.
func (c *Conn) Notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(message{JSONRPC: Version, Method: method, Params: p})
}

// OnClose registers f to run when the connection closes and returns an ID
// for Release. If the connection is already closed, f runs at once.
func (c *Conn) OnClose(f func()) uint64 {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		f()
		return 0
	}
	c.nextID++
	id := c.nextID
	c.closers[id] = f
	c.mu.Unlock()
	return id
}

// Release unregisters and returns the function registered by OnClose with id,
// without running it, or nil.
func (c *Conn) Release(id uint64) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	f := c.closers[id]
	delete(c.closers, id)
	return f
}

func (c *Conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.nc.Write(b)
	return err
}

// close closes the socket and cancels the connection's context; serve then
// winds down.
func (c *Conn) close() {
	c.cancel()
	_ = c.nc.Close()
}

func (c *Conn) serve() {
	defer func() {
		c.close()
		c.wg.Wait()
		c.mu.Lock()
		c.closed = true
		closers := c.closers
		c.closers = nil
		c.mu.Unlock()
		for _, f := range closers {
			f()
		}
		c.bg.Wait()
	}()
	r := bufio.NewReaderSize(c.nc, 4096)
	for {
		line, err := readLine(r, MaxMessage)
		if errors.Is(err, errTooLong) {
			_ = c.write(errorResponse(nil, Errorf(CodeParseError, "message longer than %d bytes", MaxMessage)))
			return
		}
		if len(bytes.TrimSpace(line)) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			return
		}
	}
}

var errTooLong = errors.New("api: message too long")

// readLine returns the next line without its newline. At EOF it returns
// what was left together with the error.
func readLine(r *bufio.Reader, maxLen int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(line)+len(chunk) > maxLen+1 {
			return nil, errTooLong
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimSuffix(line, []byte("\n")), err
	}
}

// dispatch handles one line: a single message or a batch.
func (c *Conn) dispatch(line []byte) {
	line = bytes.TrimSpace(line)
	if line[0] != '[' {
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = c.write(errorResponse(nil, parseOrInvalid(err)))
			return
		}
		c.wg.Go(func() {
			resp, after := c.call(&req)
			if resp != nil {
				_ = c.write(resp)
			}
			after()
		})
		return
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(line, &raws); err != nil {
		_ = c.write(errorResponse(nil, parseOrInvalid(err)))
		return
	}
	if len(raws) == 0 {
		_ = c.write(errorResponse(nil, Errorf(CodeInvalidRequest, "empty batch")))
		return
	}
	c.wg.Go(func() {
		resps := make([]*Response, len(raws))
		afters := make([]func(), len(raws))
		var wg sync.WaitGroup
		for i, raw := range raws {
			wg.Go(func() {
				afters[i] = func() {}
				var req Request
				if err := json.Unmarshal(raw, &req); err != nil {
					resps[i] = errorResponse(nil, Errorf(CodeInvalidRequest, "invalid request: %v", err))
					return
				}
				resps[i], afters[i] = c.call(&req)
			})
		}
		wg.Wait()
		defer func() {
			for _, f := range afters {
				f()
			}
		}()
		out := make([]*Response, 0, len(resps))
		for _, r := range resps {
			if r != nil {
				out = append(out, r)
			}
		}
		if len(out) > 0 {
			_ = c.write(out)
		}
	})
}

func parseOrInvalid(err error) *Error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return Errorf(CodeParseError, "parse error: %v", err)
	}
	return Errorf(CodeInvalidRequest, "invalid request: %v", err)
}

// call runs one request and returns its response (nil for a notification)
// and a function to run once the response has been written.
func (c *Conn) call(req *Request) (*Response, func()) {
	noop := func() {}
	if req.JSONRPC != Version || req.Method == "" || (!req.IsNotification() && !validID(req.ID)) {
		id := req.ID
		if !validID(id) {
			id = nil
		}
		return errorResponse(id, Errorf(CodeInvalidRequest, `invalid request: need "jsonrpc": "2.0", a method and a string or number id`)), noop
	}
	h := c.s.handler(req.Method)
	if h == nil {
		if req.IsNotification() {
			return nil, noop
		}
		return errorResponse(req.ID, Errorf(CodeMethodNotFound, "method not found: %s", req.Method)), noop
	}
	result, err := h(c.ctx, c, req.Params)
	after := noop
	if d, ok := result.(deferredResult); ok {
		result, after = d.v, d.after
	}
	if req.IsNotification() {
		return nil, after
	}
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Code: CodeFailed, Message: err.Error()}
		}
		return errorResponse(req.ID, e), after
	}
	if result == nil {
		result = struct{}{}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return errorResponse(req.ID, Errorf(CodeInternal, "encode result: %v", err)), after
	}
	return &Response{JSONRPC: Version, ID: req.ID, Result: b}, after
}

func errorResponse(id json.RawMessage, e *Error) *Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return &Response{JSONRPC: Version, ID: id, Error: e}
}

// DecodeParams decodes params into v strictly: unknown members are an
// invalid-params error, so typos don't pass silently. Absent params leave v
// unchanged.
func DecodeParams(params json.RawMessage, v any) error {
	if len(params) == 0 || bytes.Equal(params, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return Errorf(CodeInvalidParams, "invalid params: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Errorf(CodeInvalidParams, "invalid params: trailing data")
	}
	return nil
}
