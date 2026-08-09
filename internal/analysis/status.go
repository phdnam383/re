package analysis

import (
	"errors"
	"fmt"
)

// ErrInconsistentStatus reports a context/RCA status pair that cannot both be
// true. It is an engine bug, never a caller's problem, so it maps to an
// internal error rather than to anything the caller could act on.
var ErrInconsistentStatus = errors.New("inconsistent analysis status")

// deriveOverallStatus reduces the two stage statuses to the one the caller
// acts on, and rejects the pairs that contradict each other.
//
// Overall equals the RCA status. That is not a shortcut: the rule engine
// already lowers itself to PARTIAL when it ran over an incomplete context, so
// it is the stage that saw both the evidence and the gaps. Recomputing the
// answer here from the context status could only produce a second opinion from
// strictly less information.
//
// The contradictions are checked rather than assumed. A PARTIAL context with a
// COMPLETE analysis says the rules reached a conclusive answer from data that
// was known to be incomplete, and shipping that as a business response would
// tell an operator the engine was sure when it was not. Failing is the only
// honest outcome, because there is no way to tell from here which of the two
// statuses is the wrong one.
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
		// A failed analysis always comes back with an error, so reaching here
		// means an error was dropped somewhere between the engine and this
		// call — and the response being assembled would look successful.
		return "", fmt.Errorf("%w: rca status is FAILED without an error", ErrInconsistentStatus)
	default:
		return "", fmt.Errorf("%w: rca status %q is not recognised", ErrInconsistentStatus, rcaStatus)
	}

	if contextStatus == StatusPartial && rcaStatus != RCAStatusPartial {
		return "", fmt.Errorf("%w: context is PARTIAL but rca is %s", ErrInconsistentStatus, rcaStatus)
	}

	return rcaStatus, nil
}
