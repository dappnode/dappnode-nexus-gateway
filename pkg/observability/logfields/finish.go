package logfields

// FinishReason allows protocol values only; upstream strings are untrusted.
// This sanitizes logs without changing the response or stored usage event.
func FinishReason(reason *string) *string {
	if reason == nil {
		return nil
	}
	switch *reason {
	case "stop", "length", "tool_calls", "function_call", "content_filter":
		return reason
	default:
		unknown := "unknown"
		return &unknown
	}
}
