package api

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/permissions"
)

func confirmClient(t *testing.T, conf Confirmations) *Client {
	t.Helper()
	s := NewServer()
	Register(s, Deps{Confirm: conf})
	a, b := net.Pipe()
	s.ServeConn(a)
	c := NewClient(b)
	t.Cleanup(func() {
		_ = c.Close()
		s.Close()
	})
	return c
}

// A socket client lists confirmations and answers them; its answer counts
// as a click, so it resolves a click-only setting request.
func TestConfirmListAndAnswer(t *testing.T) {
	broker := &permissions.Broker{Timeout: time.Minute}
	t.Cleanup(broker.Close)
	c := confirmClient(t, broker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var empty json.RawMessage
	if err := c.Call(ctx, "confirm.list", nil, &empty); err != nil || string(empty) != `{"requests":[]}` {
		t.Fatalf("confirm.list = %s, %v", empty, err)
	}

	type answer struct {
		allowed bool
		how     string
	}
	done := make(chan answer, 2)
	ask := func() string {
		id, err := broker.AskSetting(core.ConfirmRequest{Key: "permissions.action_log", Old: "true", New: "false", Reason: "it affects security"},
			func(allowed bool, how string) { done <- answer{allowed, how} })
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id1, id2 := ask(), ask()

	var res ConfirmListResult
	if err := c.Call(ctx, "confirm.list", nil, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Requests) != 2 || res.Requests[0].ID != id1 || res.Requests[1].ID != id2 || !res.Requests[0].ClickOnly ||
		res.Requests[0].Kind != "setting" || res.Requests[0].Detail != "permissions.action_log: true → false" {
		t.Fatalf("confirm.list = %+v", res)
	}

	for _, tc := range []struct {
		id    string
		allow bool
	}{{id1, true}, {id2, false}} {
		allow := tc.allow
		var ar ConfirmAnswerResult
		if err := c.Call(ctx, "confirm.answer", ConfirmAnswerParams{ID: tc.id, Allow: &allow}, &ar); err != nil {
			t.Fatal(err)
		}
		if ar != (ConfirmAnswerResult{ID: tc.id, Allowed: allow}) {
			t.Errorf("confirm.answer = %+v", ar)
		}
		select {
		case a := <-done:
			if a != (answer{allow, permissions.HowClick}) {
				t.Errorf("answer %+v", a)
			}
		case <-ctx.Done():
			t.Fatal("not resolved")
		}
	}

	yes := true
	if err := c.Call(ctx, "confirm.answer", ConfirmAnswerParams{ID: id1, Allow: &yes}, nil); code(err) != CodeNotFound {
		t.Errorf("answered twice: %v", err)
	}
	if err := c.Call(ctx, "confirm.answer", map[string]string{"id": "1"}, nil); code(err) != CodeInvalidParams {
		t.Errorf("no allow: %v", err)
	}
	if err := c.Call(ctx, "confirm.answer", ConfirmAnswerParams{Allow: &yes}, nil); code(err) != CodeInvalidParams {
		t.Errorf("no id: %v", err)
	}
}

func TestConfirmUnavailable(t *testing.T) {
	c := confirmClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	yes := true
	if err := c.Call(ctx, "confirm.list", nil, nil); code(err) != CodeUnavailable {
		t.Errorf("confirm.list: %v", err)
	}
	if err := c.Call(ctx, "confirm.answer", ConfirmAnswerParams{ID: "1", Allow: &yes}, nil); code(err) != CodeUnavailable {
		t.Errorf("confirm.answer: %v", err)
	}
}
