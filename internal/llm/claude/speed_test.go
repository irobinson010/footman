package claude_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/irobinson010/footman/internal/llm"
	"github.com/irobinson010/footman/internal/llm/claude"
)

// chatBody sends one request with the given options and model and returns
// the request body the API saw.
func chatBody(t *testing.T, o claude.Options, model string) map[string]any {
	t.Helper()
	api := NewFakeAPI(t, join(start(10, 0), textBlock(0, "Hi."), finish("end_turn", 2)))
	o.BaseURL = api.URL
	o.APIKey = func(context.Context) (string, error) { return "sk-test-secret", nil }
	s, err := claude.New(o).Chat(context.Background(), llm.Request{Model: model, Messages: []llm.Message{{Role: llm.User, Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Collect(s); err != nil {
		t.Fatal(err)
	}
	return api.Bodies[0]
}

func effortOf(body map[string]any) any {
	oc, _ := body["output_config"].(map[string]any)
	return oc["effort"]
}

func TestEffortDefaultsToLowForModelsThatTakeIt(t *testing.T) {
	// A voice reply is a sentence or two: Haiku 5.5 would think first at its
	// default (medium) effort, which delays the first spoken word.
	for _, model := range []string{"", "claude-haiku-5-5", "claude-sonnet-5-5", "claude-opus-5-5"} {
		if got := effortOf(chatBody(t, claude.Options{}, model)); got != "low" {
			t.Errorf("model %q: effort = %v, want low", model, got)
		}
	}
}

func TestEffortIsLeftOutWhereItIsNotSupportedOrAsked(t *testing.T) {
	// Haiku 4.5 rejects the parameter.
	if got := effortOf(chatBody(t, claude.Options{}, "claude-haiku-4-5")); got != nil {
		t.Errorf("haiku 4.5: effort = %v, want none", got)
	}
	if got := effortOf(chatBody(t, claude.Options{}, "some-other-model")); got != nil {
		t.Errorf("unknown model: effort = %v, want none", got)
	}
	if got := effortOf(chatBody(t, claude.Options{Effort: claude.EffortDefault}, "")); got != nil {
		t.Errorf("effort default: effort = %v, want none", got)
	}
}

func TestEffortIsConfigurable(t *testing.T) {
	if got := effortOf(chatBody(t, claude.Options{Effort: "high"}, "claude-haiku-5-5")); got != "high" {
		t.Errorf("effort = %v, want high", got)
	}
}

func TestThinkingIsNotDisabled(t *testing.T) {
	// {type: disabled} is a 400 on Sonnet 5.5 and Opus 5.5, so latency is
	// tuned with effort alone.
	if th := chatBody(t, claude.Options{}, "claude-haiku-5-5")["thinking"]; th != nil {
		t.Errorf("thinking = %v, want none", th)
	}
}

func TestAPIKeyIsKeptBetweenRequests(t *testing.T) {
	// The key comes from the keyring, several D-Bus calls: not before every
	// request.
	api := NewFakeAPI(t, join(start(1, 0), textBlock(0, "a"), finish("end_turn", 1)), join(start(1, 0), textBlock(0, "b"), finish("end_turn", 1)))
	var n atomic.Int32
	p := claude.New(claude.Options{BaseURL: api.URL, APIKey: func(context.Context) (string, error) {
		n.Add(1)
		return "sk-test-secret", nil
	}})
	for range 2 {
		s, err := p.Chat(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := llm.Collect(s); err != nil {
			t.Fatal(err)
		}
	}
	if n.Load() != 1 {
		t.Errorf("key resolved %d times, want 1", n.Load())
	}
	if api.Keys[1] != "sk-test-secret" {
		t.Errorf("second request key = %q", api.Keys[1])
	}
}

func TestKeyFailureIsNotKept(t *testing.T) {
	api := NewFakeAPI(t, join(start(1, 0), textBlock(0, "a"), finish("end_turn", 1)))
	var fail atomic.Bool
	fail.Store(true)
	p := claude.New(claude.Options{BaseURL: api.URL, APIKey: func(context.Context) (string, error) {
		if fail.Load() {
			return "", errors.New("keyring locked")
		}
		return "sk-test-secret", nil
	}})
	req := llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}}
	if _, err := p.Chat(context.Background(), req); err == nil {
		t.Fatal("want the key error")
	}
	fail.Store(false) // the key is stored after all
	s, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Collect(s); err != nil {
		t.Fatal(err)
	}
}

func TestRefusedKeyIsForgotten(t *testing.T) {
	var calls, keys atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(join(start(1, 0), textBlock(0, "ok"), finish("end_turn", 1))))
	}))
	t.Cleanup(srv.Close)
	p := claude.New(claude.Options{BaseURL: srv.URL, MaxRetries: 1, APIKey: func(context.Context) (string, error) {
		keys.Add(1)
		return "sk-test-secret", nil
	}})
	req := llm.Request{Messages: []llm.Message{{Role: llm.User, Text: "hi"}}}
	s, err := p.Chat(context.Background(), req)
	if err == nil {
		_, err = llm.Collect(s)
	}
	if err == nil {
		t.Fatal("want the 401")
	}
	s, err = p.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Collect(s); err != nil {
		t.Fatal(err)
	}
	if keys.Load() != 2 {
		t.Errorf("key resolved %d times, want 2 (again after the 401)", keys.Load())
	}
}

func TestWarmResolvesKeyAndConnectsWithoutCredentials(t *testing.T) {
	var heads, withKey atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			heads.Add(1)
		}
		if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			withKey.Add(1)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	var keys atomic.Int32
	p := claude.New(claude.Options{BaseURL: srv.URL, APIKey: func(context.Context) (string, error) {
		keys.Add(1)
		return "sk-test-secret", nil
	}})
	p.Warm(context.Background())
	p.Warm(context.Background()) // a second warm-up soon after does nothing
	if heads.Load() != 1 || keys.Load() != 1 {
		t.Errorf("heads = %d, keys = %d, want 1 each", heads.Load(), keys.Load())
	}
	if withKey.Load() != 0 {
		t.Error("the warm-up request carried credentials")
	}
}

func TestWarmNeedsAKeyAndNeverFails(t *testing.T) {
	var heads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { heads.Add(1) }))
	t.Cleanup(srv.Close)
	claude.New(claude.Options{BaseURL: srv.URL}).Warm(context.Background())
	claude.New(claude.Options{BaseURL: srv.URL, APIKey: func(context.Context) (string, error) { return "", errors.New("locked") }}).Warm(context.Background())
	if heads.Load() != 0 {
		t.Errorf("connected %d times without a key", heads.Load())
	}
}
