package analysis

import (
	"errors"
	"fmt"
)

var ErrInconsistentStatus = errors.New("inconsistent analysis status")

func deriveOverallStatus(contextStatus, rcaStatus string) (string, error) {
	switch contextStatus {
	case StatusComplete, StatusPartial:
	default:
		return "", fmt.Errorf("%w: context status %q is not COMPLETE or PARTIAL",
			ErrInconsistentStatus, contextStatus)
	}

	switch rcaStatus {
	case RCAStatusComplete, RCAStatusNoConclusion, RCAStatusPartial:
	case RCAStatusFailed:

		return "", fmt.Errorf("%w: rca status is FAILED without an error", ErrInconsistentStatus)
	default:
		return "", fmt.Errorf("%w: rca status %q is not recognised", ErrInconsistentStatus, rcaStatus)
	}

	if contextStatus == StatusPartial && rcaStatus != RCAStatusPartial {
		return "", fmt.Errorf("%w: context is PARTIAL but rca is %s", ErrInconsistentStatus, rcaStatus)
	}

	return rcaStatus, nil
}
