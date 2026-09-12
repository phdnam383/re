package analysis

import (
	"errors"
	"fmt"
)

var ErrInvalidRequest = errors.New("invalid request")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

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
