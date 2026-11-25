// Package bus is the in-process event bus that decouples the pipeline, jobs,
// the local API and the UI (design §5, §30). Components publish typed events
// on named topics and subscribe to the ones they care about.
//
// Publishing never blocks: each subscriber has a bounded buffer, and an event
// that does not fit is dropped for that subscriber and counted, so one slow
// consumer (a stalled WebSocket client, say) cannot hold up the pipeline.
//
//	var StateChanged = bus.NewTopic[core.StateChange]("pipeline.state")
//
//	sub := bus.Subscribe(b, StateChanged, 64)
//	defer sub.Close()
//	for ev := range sub.C() { ... ev.Data ... }
//
//	bus.Publish(b, StateChanged, core.StateChange{From: core.Idle, To: core.Listening})
//
// SubscribeAll receives every topic untyped, for the local API's event stream
// and the logs.
package bus
