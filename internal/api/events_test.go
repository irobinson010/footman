package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/platform"
)

func TestEventData(t *testing.T) {
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		in   any
		want string
	}{
		{core.StateChange{From: core.Idle, To: core.Listening}, `{"from":"idle","to":"listening","turn":0}`},
		{core.Transcript{Turn: 2, Trigger: core.TriggerWake, Text: "hi"}, `{"turn":2,"trigger":"wake","text":"hi"}`},
		{core.Routed{Turn: 2, Route: core.Route{Kind: core.RouteDictation, Text: "x"}}, `{"turn":2,"route":"dictation","text":"x"}`},
		{core.Insertion{Turn: 1, Text: "x", Method: "paste"}, `{"turn":1,"text":"x","method":"paste"}`},
		{core.FastPathRan{Turn: 1, Rule: "open", Err: "no"}, `{"turn":1,"rule":"open","error":"no"}`},
		{core.ToolRan{Turn: 1, Name: "t", Args: json.RawMessage(`{"a":1}`), Result: "ok"}, `{"turn":1,"name":"t","args":{"a":1},"result":"ok","is_error":false}`},
		{core.Reply{Turn: 1, Text: "Done."}, `{"turn":1,"text":"Done."}`},
		{core.Stopped{Source: core.StopHotkey}, `{"source":"hotkey","jobs":[],"turns":0}`},
		{core.Failure{Turn: 3, State: core.Thinking, Err: "x"}, `{"turn":3,"state":"thinking","error":"x"}`},
		{platform.WakeListening, `{"mic":"wake_listening"}`},
		{platform.Recording, `{"mic":"recording"}`},
		{core.JobEvent{ID: "job-1", Prompt: "p", Update: jobs.Update{Status: jobs.Succeeded, Message: "done", At: at}},
			`{"id":"job-1","prompt":"p","status":"succeeded","message":"done","at":"2026-11-25T09:00:00Z"}`},
		{map[string]int{"other": 1}, `{"other":1}`},
	}
	for _, tt := range tests {
		ev, err := toEvent("sub-1", bus.Event[any]{Topic: "t", Seq: 1, At: at, Data: tt.in})
		if err != nil {
			t.Errorf("%T: %v", tt.in, err)
			continue
		}
		if string(ev.Data) != tt.want {
			t.Errorf("%T:\n got %s\nwant %s", tt.in, ev.Data, tt.want)
		}
	}
}

func TestTopicFilter(t *testing.T) {
	tests := []struct {
		f     topicFilter
		topic string
		want  bool
	}{
		{nil, "pipeline.state", true},
		{topicFilter{"*"}, "jobs.update", true},
		{topicFilter{"pipeline.state"}, "pipeline.state", true},
		{topicFilter{"pipeline.state"}, "pipeline.stateful", false},
		{topicFilter{"pipeline.*"}, "pipeline.state", true},
		{topicFilter{"pipeline.*"}, "jobs.update", false},
		{topicFilter{"pipe*"}, "pipeline.state", false},
		{topicFilter{"jobs.update", "pipeline.*"}, "jobs.update", true},
	}
	for _, tt := range tests {
		if got := tt.f.match(tt.topic); got != tt.want {
			t.Errorf("%v.match(%q) = %v, want %v", tt.f, tt.topic, got, tt.want)
		}
	}
}
