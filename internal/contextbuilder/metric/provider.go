package metric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

const DefaultTimeout = 2 * time.Second

const maxBodyBytes = 1 << 20

const baseURL = "http://metrics"

const valueField = "value"

type Options struct {
	Client *http.Client

	Timeout time.Duration

	Clock contextbuilder.Clock

	BaseURL string
}

type Provider struct {
	client  *http.Client
	timeout time.Duration
	clock   contextbuilder.Clock
	baseURL string
}

func New(opts Options) *Provider {
	p := &Provider{client: opts.Client, timeout: opts.Timeout, clock: opts.Clock, baseURL: opts.BaseURL}
	if p.client == nil {
		p.client = &http.Client{}
	}
	if p.timeout <= 0 {
		p.timeout = DefaultTimeout
	}
	if p.clock == nil {
		p.clock = contextbuilder.SystemClock()
	}
	if p.baseURL == "" {
		p.baseURL = baseURL
	}
	return p
}

func (p *Provider) FetchMetrics(ctx context.Context, vnfcPath string, names []string) (contextbuilder.MetricResult, error) {
	if len(names) == 0 {
		return contextbuilder.MetricResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return contextbuilder.MetricResult{}, err
	}

	entries := make([]*analysis.MetricEntry, len(names))
	missing := make([]*analysis.MissingContext, len(names))

	vduPath := vduPathOf(vnfcPath)

	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, miss := p.fetchOne(ctx, p.baseURL, vduPath, vnfcPath, name)
			entries[i], missing[i] = entry, miss
		}()
	}
	wg.Wait()

	var result contextbuilder.MetricResult
	for i := range names {
		if entries[i] != nil {
			result.Entries = append(result.Entries, *entries[i])
		}
		if missing[i] != nil {
			result.Missing = append(result.Missing, *missing[i])
		}
	}
	return result, nil
}

func vduPathOf(vnfcPath string) string {
	if i := strings.LastIndex(vnfcPath, "."); i >= 0 {
		return vnfcPath[:i]
	}
	return vnfcPath
}

func buildMetricURL(base, vduPath, name, vnfcPath string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u = u.JoinPath(vduPath+"."+name, "value")
	q := u.Query()
	q.Set("vnfc_path", vnfcPath)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (p *Provider) fetchOne(ctx context.Context, base, vduPath, vnfcPath, name string) (*analysis.MetricEntry, *analysis.MissingContext) {
	reqURL, err := buildMetricURL(base, vduPath, name, vnfcPath)
	if err != nil {
		return nil, missingFor(vnfcPath, name, analysis.ReasonRequestFailed)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, missingFor(vnfcPath, name, analysis.ReasonRequestFailed)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, missingFor(vnfcPath, name, analysis.ReasonTimeout)
		}
		return nil, missingFor(vnfcPath, name, analysis.ReasonRequestFailed)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, missingFor(vnfcPath, name, analysis.ReasonTimeout)
		}
		return nil, missingFor(vnfcPath, name, analysis.ReasonRequestFailed)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, missingFor(vnfcPath, name, analysis.ReasonHTTPStatus)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, missingFor(vnfcPath, name, analysis.ReasonEmptyBody)
	}

	value, err := decodeValue(body)
	if err != nil {
		return nil, missingFor(vnfcPath, name, analysis.ReasonInvalidJSON)
	}

	return &analysis.MetricEntry{
		Name:   name,
		Value:  value,
		ReadAt: p.clock.Now().UTC(),
	}, nil
}

func decodeValue(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing content after the metric JSON")
	}
	v, ok := obj[valueField]
	if !ok {
		return nil, errors.New("metric response is missing the value field")
	}
	return v, nil
}

func missingFor(vnfcPath, name, reason string) *analysis.MissingContext {
	return &analysis.MissingContext{
		Provider: analysis.ProviderMetric,
		Entity:   vnfcPath,
		Key:      name,
		Reason:   reason,
	}
}

var _ contextbuilder.MetricProvider = (*Provider)(nil)
