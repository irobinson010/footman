package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeServer starts s on one end of an in-memory connection and returns the
// other end.
func pipeServer(t *testing.T, s *Server) net.Conn {
	t.Helper()
	a, b := net.Pipe()
	s.ServeConn(a)
	t.Cleanup(func() {
		_ = b.Close()
		s.Close()
	})
	return b
}

func testServer() *Server {
	s := NewServer()
	s.Handle("echo", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		var p struct {
			Text string `json:"text"`
		}
		if err := DecodeParams(params, &p); err != nil {
			return nil, err
		}
		return p, nil
	})
	s.Handle("fail", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		return nil, errors.New("it broke")
	})
	s.Handle("nothing", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		return nil, nil
	})
	return s
}

// raw sends lines and reads one line back per expected reply.
type raw struct {
	t  *testing.T
	nc net.Conn
	r  *bufio.Reader
}

func newRaw(t *testing.T, nc net.Conn) *raw {
	return &raw{t: t, nc: nc, r: bufio.NewReader(nc)}
}

func (r *raw) send(line string) {
	r.t.Helper()
	if _, err := r.nc.Write([]byte(line + "\n")); err != nil {
		r.t.Fatalf("write: %v", err)
	}
}

func (r *raw) recv() string {
	r.t.Helper()
	_ = r.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.r.ReadString('\n')
	if err != nil {
		r.t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(line)
}

func TestWireFormat(t *testing.T) {
	r := newRaw(t, pipeServer(t, testServer()))
	tests := []struct {
		name, send, want string
	}{
		{"result", `{"jsonrpc":"2.0","id":1,"method":"echo","params":{"text":"hi"}}`,
			`{"jsonrpc":"2.0","id":1,"result":{"text":"hi"}}`},
		{"string id", `{"jsonrpc":"2.0","id":"a","method":"nothing"}`,
			`{"jsonrpc":"2.0","id":"a","result":{}}`},
		{"method not found", `{"jsonrpc":"2.0","id":2,"method":"nope"}`,
			`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"method not found: nope"}}`},
		{"plain error", `{"jsonrpc":"2.0","id":3,"method":"fail"}`,
			`{"jsonrpc":"2.0","id":3,"error":{"code":-32000,"message":"it broke"}}`},
		{"parse error", `{"jsonrpc":`,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error: unexpected end of JSON input"}}`},
		{"no version", `{"id":4,"method":"echo"}`,
			`{"jsonrpc":"2.0","id":4,"error":{"code":-32600,"message":"invalid request: need \"jsonrpc\": \"2.0\", a method and a string or number id"}}`},
		{"bad id", `{"jsonrpc":"2.0","id":{},"method":"echo"}`,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request: need \"jsonrpc\": \"2.0\", a method and a string or number id"}}`},
		{"empty batch", `[]`,
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"empty batch"}}`},
	}
	for _, tt := range tests {
		r.send(tt.send)
		if got := r.recv(); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestInvalidParams(t *testing.T) {
	r := newRaw(t, pipeServer(t, testServer()))
	r.send(`{"jsonrpc":"2.0","id":1,"method":"echo","params":{"txt":"typo"}}`)
	var resp Response
	if err := json.Unmarshal([]byte(r.recv()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "txt") {
		t.Errorf("error = %+v, want invalid params naming the unknown field", resp.Error)
	}
}

func TestNotificationsGetNoReply(t *testing.T) {
	r := newRaw(t, pipeServer(t, testServer()))
	r.send(`{"jsonrpc":"2.0","method":"echo","params":{"text":"quiet"}}`)
	r.send(`{"jsonrpc":"2.0","method":"nope"}`)
	r.send(`{"jsonrpc":"2.0","id":9,"method":"nothing"}`)
	// The first reply is the one to the request: the notifications got none.
	if got, want := r.recv(), `{"jsonrpc":"2.0","id":9,"result":{}}`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestBatch(t *testing.T) {
	r := newRaw(t, pipeServer(t, testServer()))
	r.send(`[{"jsonrpc":"2.0","id":1,"method":"echo","params":{"text":"a"}},` +
		`{"jsonrpc":"2.0","method":"echo"},` +
		`{"jsonrpc":"2.0","id":2,"method":"nope"},` +
		`5]`)
	var resps []Response
	if err := json.Unmarshal([]byte(r.recv()), &resps); err != nil {
		t.Fatal(err)
	}
	if len(resps) != 3 {
		t.Fatalf("got %d responses, want 3 (the notification gets none): %+v", len(resps), resps)
	}
	if string(resps[0].ID) != "1" || string(resps[0].Result) != `{"text":"a"}` {
		t.Errorf("first = %+v", resps[0])
	}
	if resps[1].Error == nil || resps[1].Error.Code != CodeMethodNotFound {
		t.Errorf("second = %+v", resps[1])
	}
	if resps[2].Error == nil || resps[2].Error.Code != CodeInvalidRequest {
		t.Errorf("third = %+v", resps[2])
	}
}

func TestTooLongClosesConnection(t *testing.T) {
	r := newRaw(t, pipeServer(t, testServer()))
	go func() {
		// The server stops reading part way, so this write may not finish.
		_, _ = r.nc.Write([]byte(`"` + strings.Repeat("x", MaxMessage+10) + "\"\n"))
	}()
	if got := r.recv(); !strings.Contains(got, `"code":-32700`) {
		t.Errorf("got %s, want a parse error", got)
	}
	if _, err := r.r.ReadString('\n'); err == nil {
		t.Error("connection still open after an oversized message")
	}
}

func TestClientConcurrentCalls(t *testing.T) {
	s := testServer()
	block := make(chan struct{})
	s.Handle("wait", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		<-block
		return "waited", nil
	})
	c := NewClient(pipeServer(t, s))
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	// A slow call does not hold up others on the same connection.
	slow := make(chan error, 1)
	go func() {
		var got string
		err := c.Call(ctx, "wait", nil, &got)
		if err == nil && got != "waited" {
			err = errors.New("wrong result " + got)
		}
		slow <- err
	}()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			text := strings.Repeat("x", i)
			var got struct{ Text string }
			if err := c.Call(ctx, "echo", map[string]string{"text": text}, &got); err != nil || got.Text != text {
				t.Errorf("echo %d: %q, %v", i, got.Text, err)
			}
		})
	}
	wg.Wait()
	close(block)
	if err := <-slow; err != nil {
		t.Errorf("slow call: %v", err)
	}

	err := c.Call(ctx, "fail", nil, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeFailed || e.Message != "it broke" {
		t.Errorf("fail: %v", err)
	}
}

func TestCloseCancelsHandlers(t *testing.T) {
	s := testServer()
	started := make(chan struct{})
	s.Handle("hang", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	a, b := net.Pipe()
	s.ServeConn(a)
	c := NewClient(b)
	defer func() { _ = c.Close() }()
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "hang", nil, nil) }()
	<-started
	s.Close() // returns only once the handler has
	if err := <-errc; !errors.Is(err, ErrClosed) {
		t.Errorf("call after server Close = %v, want ErrClosed", err)
	}
	<-c.Done()
	if err := c.Call(context.Background(), "echo", nil, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("call on closed client = %v", err)
	}
}

func TestConnOnClose(t *testing.T) {
	s := NewServer()
	ran := make(chan string, 2)
	s.Handle("register", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		c.OnClose(func() { ran <- "kept" })
		id := c.OnClose(func() { ran <- "released" })
		if c.Release(id) == nil {
			t.Error("Release returned nil")
		}
		return nil, nil
	})
	a, b := net.Pipe()
	s.ServeConn(a)
	c := NewClient(b)
	if err := c.Call(context.Background(), "register", nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	s.Close()
	close(ran)
	var got []string
	for r := range ran {
		got = append(got, r)
	}
	if len(got) != 1 || got[0] != "kept" {
		t.Errorf("OnClose ran %v, want [kept]", got)
	}
}

func TestDuplicateMethodPanics(t *testing.T) {
	s := testServer()
	defer func() {
		if recover() == nil {
			t.Error("registering echo twice did not panic")
		}
	}()
	s.Handle("echo", nil)
}

func TestCallContextCancelled(t *testing.T) {
	s := NewServer()
	release := make(chan struct{})
	s.Handle("hang", func(ctx context.Context, c *Conn, params json.RawMessage) (any, error) {
		<-release
		return nil, nil
	})
	c := NewClient(pipeServer(t, s))
	defer func() { _ = c.Close() }()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Call(ctx, "hang", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Call = %v, want context.Canceled", err)
	}
}
