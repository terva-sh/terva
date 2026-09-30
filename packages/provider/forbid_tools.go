package provider

// withholdToolCalls enforces Request.ForbidTools for a client that keeps the
// tools on the wire and sends no tool_choice. The model can still answer with
// a call. This forwards every event except the call: the tool events are
// dropped, and the tool-call blocks leave the assembled message. The stop
// reason stays StopToolUse, so a caller that reads it can tell that the model
// tried a call and got none.
func withholdToolCalls(in <-chan Event) <-chan Event {
	out := make(chan Event, cap(in))
	go func() {
		defer close(out)
		for ev := range in {
			switch e := ev.(type) {
			case EventToolStart, EventToolArgs, EventToolEnd:
				continue
			case EventDone:
				e.Message.Content = withoutToolCalls(e.Message.Content)
				ev = e
			}
			out <- ev
		}
	}()
	return out
}

func withoutToolCalls(content []Content) []Content {
	var kept []Content
	for _, c := range content {
		if _, ok := c.(ToolCallBlock); ok {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}
