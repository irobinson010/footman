package api

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/core"
	"github.com/irobinson010/footman/internal/platform"
)

// EventMethod is the notification method events are pushed with.
const EventMethod = "event"

// Event is the params of an "event" notification.
type Event struct {
	// Subscription is the ID events.subscribe returned.
	Subscription string `json:"subscription"`
	// Topic is the bus topic, such as "pipeline.state".
	Topic string `json:"topic"`
	// Seq numbers events across the daemon in publish order. A gap means the
	// client missed events, because it fell behind or is not subscribed to
	// the topics in between.
	Seq uint64 `json:"seq"`
	// At is when the event was published.
	At time.Time `json:"at"`
	// Data is the topic's payload (see docs/api/v1.md).
	Data json.RawMessage `json:"data"`
}

// Payloads of the core topics, as the API shows them. They are separate from
// the core types so the public API keeps its snake_case names and string
// enums however the core changes.
type (
	stateData struct {
		From string `json:"from"`
		To   string `json:"to"`
		Turn uint64 `json:"turn"`
	}
	transcriptData struct {
		Turn    uint64 `json:"turn"`
		Trigger string `json:"trigger"`
		Text    string `json:"text"`
	}
	routeData struct {
		Turn  uint64 `json:"turn"`
		Route string `json:"route"`
		Text  string `json:"text"`
	}
	insertedData struct {
		Turn   uint64 `json:"turn"`
		Text   string `json:"text"`
		Method string `json:"method"`
	}
	fastPathData struct {
		Turn  uint64 `json:"turn"`
		Rule  string `json:"rule"`
		Error string `json:"error,omitempty"`
	}
	toolData struct {
		Turn    uint64          `json:"turn"`
		Name    string          `json:"name"`
		Args    json.RawMessage `json:"args,omitempty"`
		Result  string          `json:"result"`
		IsError bool            `json:"is_error"`
	}
	replyData struct {
		Turn uint64 `json:"turn"`
		Text string `json:"text"`
	}
	stopData struct {
		Source string   `json:"source"`
		Jobs   []string `json:"jobs"`
		Turns  int      `json:"turns"`
	}
	failureData struct {
		Turn  uint64 `json:"turn"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	micData struct {
		Mic string `json:"mic"`
	}
	jobData struct {
		ID      string    `json:"id"`
		Prompt  string    `json:"prompt"`
		Status  string    `json:"status"`
		Message string    `json:"message"`
		At      time.Time `json:"at"`
	}
)

// micName is the API name of a tray (microphone) status.
func micName(s platform.TrayStatus) string {
	switch s {
	case platform.MicOff:
		return "off"
	case platform.WakeListening:
		return "wake_listening"
	case platform.Recording:
		return "recording"
	}
	return "unknown"
}

// eventData converts a bus payload to its API shape. Payloads without an API
// shape of their own are sent as encoding/json renders them.
func eventData(data any) any {
	switch d := data.(type) {
	case core.StateChange:
		return stateData{From: d.From.String(), To: d.To.String(), Turn: d.Turn}
	case core.Transcript:
		return transcriptData{Turn: d.Turn, Trigger: d.Trigger.String(), Text: d.Text}
	case core.Routed:
		return routeData{Turn: d.Turn, Route: d.Route.Kind.String(), Text: d.Route.Text}
	case core.Insertion:
		return insertedData{Turn: d.Turn, Text: d.Text, Method: string(d.Method)}
	case core.FastPathRan:
		return fastPathData{Turn: d.Turn, Rule: d.Rule, Error: d.Err}
	case core.ToolRan:
		return toolData{Turn: d.Turn, Name: d.Name, Args: d.Args, Result: d.Result, IsError: d.IsError}
	case core.Reply:
		return replyData(d)
	case core.Stopped:
		jobs := d.Jobs
		if jobs == nil {
			jobs = []string{}
		}
		return stopData{Source: string(d.Source), Jobs: jobs, Turns: d.Turns}
	case core.Failure:
		return failureData{Turn: d.Turn, State: d.State.String(), Error: d.Err}
	case platform.TrayStatus:
		return micData{Mic: micName(d)}
	case core.JobEvent:
		return jobData{ID: d.ID, Prompt: d.Prompt, Status: d.Update.Status.String(), Message: d.Update.Message, At: d.Update.At}
	}
	return data
}

// topicFilter matches topics against a subscription's patterns: an exact
// name ("pipeline.state"), a prefix ending in ".*" ("pipeline.*"), or "*".
// No patterns means every topic.
type topicFilter []string

func (f topicFilter) match(topic string) bool {
	if len(f) == 0 {
		return true
	}
	for _, p := range f {
		if p == "*" || p == topic {
			return true
		}
		if prefix, ok := strings.CutSuffix(p, "*"); ok && strings.HasSuffix(prefix, ".") && strings.HasPrefix(topic, prefix) {
			return true
		}
	}
	return false
}

// toEvent converts a bus event for subscription id.
func toEvent(id string, ev bus.Event[any]) (Event, error) {
	b, err := json.Marshal(eventData(ev.Data))
	if err != nil {
		return Event{}, err
	}
	return Event{Subscription: id, Topic: ev.Topic, Seq: ev.Seq, At: ev.At, Data: b}, nil
}
