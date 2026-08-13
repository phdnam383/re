package analysis

import (
	"errors"
	"fmt"
)

// ErrInvalidRequest marks a request the caller must fix. Every validation
// failure wraps it, so the transport can map the whole class to one gRPC code
// with errors.Is instead of matching on message text.
var ErrInvalidRequest = errors.New("invalid request")

// invalidf builds a validation error naming the offending field.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// Validate reports whether the request can be analysed at all.
//
// It checks identity and nothing else. request_id is what a response is
// correlated by; an alert's id and source_path are what every downstream
// stage addresses things with — a profile selector matches on the alert, and
// the builder fetches the topology under source_path.
//
// Everything else is deliberately optional. alert_type, probable_cause,
// severity, state and additional_information are all inputs to selector
// matching, and a selector that matches nothing is a legitimate answer
// (ErrContextProfileNotFound), not a malformed request. Rejecting them here
// would move the operator's profile definitions into the transport's
// validation rules.
//
// Nothing is trimmed or case-folded. Selector matching compares values, and an
// engine that quietly normalised its input would answer a question the caller
// did not ask.
func (in ContextInput) Validate() error {
	if in.RequestID == "" {
		return invalidf("request_id is empty")
	}
	if len(in.Alerts) == 0 {
		return invalidf("alert is required")
	}
	for i, a := range in.Alerts {
		field := "alert"
		if len(in.Alerts) > 1 {
			field = fmt.Sprintf("alerts[%d]", i)
		}
		if a.ID == "" {
			return invalidf("%s.id is empty", field)
		}
		if a.SourcePath == "" {
			return invalidf("%s.source_path is empty", field)
		}
	}
	return nil
}
