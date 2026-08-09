package contextbuilder

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"re/internal/analysis"
)

// The fakes below deliberately answer in reverse request order. Nothing in the
// contract promises a provider returns targets in the order it was given, so
// every assertion about snapshot ordering in these tests is really an
// assertion that the builder sorted — not that a provider happened to.

type fakeProfiles struct {
	profiles []ContextProfile
	err      error
	calls    atomic.Int32
}

func (f *fakeProfiles) LoadEnabled(context.Context) ([]ContextProfile, error) {
	f.calls.Add(1)
	return f.profiles, f.err
}

type fakeVDUProvider struct {
	vdus  map[string]analysis.VDU
	vnfcs map[string][]analysis.VNFC
	err   error

	// before runs inside FetchVDUs, used to hold the call open while the
	// concurrency of the fan-out is observed.
	before func(context.Context)

	calls atomic.Int32
	mu    sync.Mutex
	asked []string
}

func (f *fakeVDUProvider) FetchVDUs(ctx context.Context, paths []string) (VDUResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.asked = append(f.asked, paths...)
	f.mu.Unlock()
	if f.before != nil {
		f.before(ctx)
	}
	if f.err != nil {
		return VDUResult{}, f.err
	}

	var res VDUResult
	for i := len(paths) - 1; i >= 0; i-- {
		path := paths[i]
		vdu, ok := f.vdus[path]
		if !ok {
			res.Missing = append(res.Missing, analysis.MissingContext{
				Provider: analysis.ProviderVDU, Entity: path, Reason: analysis.ReasonNotFound,
			})
			continue
		}
		res.VDUs = append(res.VDUs, vdu)
		res.VNFCs = append(res.VNFCs, f.vnfcs[path]...)
	}
	return res, nil
}

type fakeLinkProvider struct {
	links  map[LinkTarget]analysis.Link
	err    error
	before func(context.Context)

	calls atomic.Int32
	mu    sync.Mutex
	asked []LinkTarget
}

func (f *fakeLinkProvider) FetchLinks(ctx context.Context, targets []LinkTarget) (LinkResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.asked = append(f.asked, targets...)
	f.mu.Unlock()
	if f.before != nil {
		f.before(ctx)
	}
	if f.err != nil {
		return LinkResult{}, f.err
	}

	var res LinkResult
	for i := len(targets) - 1; i >= 0; i-- {
		t := targets[i]
		link, ok := f.links[t]
		if !ok {
			res.Missing = append(res.Missing, analysis.MissingContext{
				Provider: analysis.ProviderLink, Entity: t.Entity(), Reason: analysis.ReasonNotFound,
			})
			continue
		}
		res.Links = append(res.Links, link)
	}
	return res, nil
}

type fakeConfigProvider struct {
	values map[configKey]any
	err    error
	before func(context.Context)

	calls atomic.Int32
	mu    sync.Mutex
	asked []ConfigurationTarget
}

func (f *fakeConfigProvider) FetchConfiguration(ctx context.Context, targets []ConfigurationTarget) (ConfigurationResult, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.asked = append(f.asked, targets...)
	f.mu.Unlock()
	if f.before != nil {
		f.before(ctx)
	}
	if f.err != nil {
		return ConfigurationResult{}, f.err
	}

	var res ConfigurationResult
	for i := len(targets) - 1; i >= 0; i-- {
		t := targets[i]
		value, ok := f.values[configKey{path: t.Path, key: t.Key}]
		if !ok {
			res.Missing = append(res.Missing, analysis.MissingContext{
				Provider: analysis.ProviderConfiguration, Entity: t.Path, Key: t.Key,
				Reason: analysis.ReasonHTTPStatus,
			})
			continue
		}
		res.Entries = append(res.Entries, analysis.ConfigurationEntry{
			Path: t.Path, Key: t.Key, URL: t.URL, Value: value, ReadAt: fixedNow,
		})
	}
	return res, nil
}

// fixedNow is the instant every test clock reports, so BuiltAt and ReadAt are
// reproducible and the golden fixtures mean something.
var fixedNow = time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

func fixedClock() Clock { return ClockFunc(func() time.Time { return fixedNow }) }

// barrier reports whether n goroutines reached it at the same time. It is how
// the fan-out is tested without timing assumptions: if the providers ran one
// after another the first to arrive never sees the others and the wait fails,
// rather than the test guessing from a stopwatch.
type barrier struct {
	wg   sync.WaitGroup
	done chan struct{}
}

func newBarrier(n int) *barrier {
	b := &barrier{done: make(chan struct{})}
	b.wg.Add(n)
	go func() {
		b.wg.Wait()
		close(b.done)
	}()
	return b
}

func (b *barrier) arrive(timeout time.Duration) error {
	b.wg.Done()
	select {
	case <-b.done:
		return nil
	case <-time.After(timeout):
		return errors.New("providers did not run concurrently")
	}
}
