package configuration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

// DefaultTimeout caps one configuration call. It is per target, not per
// provider: the calls run concurrently, so one unresponsive NF holds up only
// its own entry.
const DefaultTimeout = 2 * time.Second

// maxBodyBytes bounds what one response may contribute. A configuration value
// is a scalar or a small document; anything larger is a misbehaving API, and
// the truncated body then fails to parse and is reported as invalid JSON.
const maxBodyBytes = 1 << 20

// Options configures the provider. The zero value is usable.
type Options struct {
	// Client defaults to a plain http.Client. Set no timeout on it — the
	// per-call deadline is applied through the request context so it composes
	// with the caller's deadline.
	Client *http.Client

	// Timeout defaults to DefaultTimeout.
	Timeout time.Duration

	// Clock stamps ConfigurationEntry.ReadAt. Defaults to the system clock.
	Clock contextbuilder.Clock
}

// Provider reads effective configuration over HTTP, one GET per target.
type Provider struct {
	client  *http.Client
	timeout time.Duration
	clock   contextbuilder.Clock
}

func New(opts Options) *Provider {
	p := &Provider{client: opts.Client, timeout: opts.Timeout, clock: opts.Clock}
	if p.client == nil {
		p.client = &http.Client{}
	}
	if p.timeout <= 0 {
		p.timeout = DefaultTimeout
	}
	if p.clock == nil {
		p.clock = contextbuilder.SystemClock()
	}
	return p
}

// FetchConfiguration issues one independent GET per target, all concurrently.
//
// There is no batch endpoint and no shared connection budget: each target
// carries its own URL, so there is nothing to batch, and a per-target failure
// must not take the others down with it. Concurrency is unbounded because the
// target list comes from profiles an operator wrote, not from a topology walk
// that could expand without limit.
//
// The method never fails as a whole once it starts — every outcome is either
// an entry or a MissingContext for that one target. The single error it can
// return is the caller's own cancellation, which is not a degraded snapshot
// but a request that no longer wants an answer.
func (p *Provider) FetchConfiguration(ctx context.Context, targets []contextbuilder.ConfigurationTarget) (contextbuilder.ConfigurationResult, error) {
	if len(targets) == 0 {
		return contextbuilder.ConfigurationResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return contextbuilder.ConfigurationResult{}, err
	}

	// Each goroutine writes only its own index, so the results land in target
	// order however the calls interleave. That is what makes a build with a
	// fixed clock byte-identical run to run — sorting afterwards would hide a
	// race rather than remove one.
	entries := make([]*analysis.ConfigurationEntry, len(targets))
	missing := make([]*analysis.MissingContext, len(targets))

	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, miss := p.fetchOne(ctx, target)
			entries[i], missing[i] = entry, miss
		}()
	}
	wg.Wait()

	var result contextbuilder.ConfigurationResult
	for i := range targets {
		if entries[i] != nil {
			result.Entries = append(result.Entries, *entries[i])
		}
		if missing[i] != nil {
			result.Missing = append(result.Missing, *missing[i])
		}
	}
	return result, nil
}

// fetchOne performs one GET and classifies the outcome. Exactly one of the
// returned pointers is non-nil.
func (p *Provider) fetchOne(ctx context.Context, t contextbuilder.ConfigurationTarget) (*analysis.ConfigurationEntry, *analysis.MissingContext) {
	// WithTimeout inherits the parent deadline, so a caller deadline shorter
	// than p.timeout still wins — the effective deadline is the earlier of the
	// two without any comparison here.
	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, t.URL, nil)
	if err != nil {
		return nil, missingFor(t, analysis.ReasonRequestFailed)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, missingFor(t, analysis.ReasonTimeout)
		}
		return nil, missingFor(t, analysis.ReasonRequestFailed)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, missingFor(t, analysis.ReasonTimeout)
		}
		return nil, missingFor(t, analysis.ReasonRequestFailed)
	}

	// Status is classified before the body: a 500 whose body happens to be
	// valid JSON is an error page, not a configuration value.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, missingFor(t, analysis.ReasonHTTPStatus)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		// Not the same as a null value. An API answering 2xx with no content
		// has not said what the effective value is, and recording nil would
		// let a rule read "the NF has this unset".
		return nil, missingFor(t, analysis.ReasonEmptyBody)
	}

	value, err := decodeSingleJSONValue(body)
	if err != nil {
		return nil, missingFor(t, analysis.ReasonInvalidJSON)
	}

	return &analysis.ConfigurationEntry{
		Path:   t.Path,
		Key:    t.Key,
		URL:    t.URL,
		Value:  value,
		ReadAt: p.clock.Now().UTC(),
	}, nil
}

// decodeSingleJSONValue requires the body to be exactly one JSON value —
// scalar, object or array. Trailing content is rejected rather than ignored:
// two concatenated documents mean the API is not answering the contract, and
// silently taking the first would put an arbitrary half of the answer into the
// snapshot.
func decodeSingleJSONValue(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing content after the JSON value")
	}
	return v, nil
}

func missingFor(t contextbuilder.ConfigurationTarget, reason string) *analysis.MissingContext {
	return &analysis.MissingContext{
		Provider: analysis.ProviderConfiguration,
		Entity:   t.Path,
		Key:      t.Key,
		Reason:   reason,
	}
}

var _ contextbuilder.ConfigurationProvider = (*Provider)(nil)
