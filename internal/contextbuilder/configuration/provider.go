package configuration

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

const baseURL = "http://config"

const currentValueField = "currentValue"

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

func (p *Provider) FetchConfiguration(ctx context.Context, sourcePath string, keys []string) (contextbuilder.ConfigurationResult, error) {
	if len(keys) == 0 {
		return contextbuilder.ConfigurationResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return contextbuilder.ConfigurationResult{}, err
	}

	entries := make([]*analysis.ConfigurationEntry, len(keys))
	missing := make([]*analysis.MissingContext, len(keys))

	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, miss := p.fetchOne(ctx, sourcePath, key)
			entries[i], missing[i] = entry, miss
		}()
	}
	wg.Wait()

	var result contextbuilder.ConfigurationResult
	for i := range keys {
		if entries[i] != nil {
			result.Entries = append(result.Entries, *entries[i])
		}
		if missing[i] != nil {
			result.Missing = append(result.Missing, *missing[i])
		}
	}
	return result, nil
}

func buildConfigURL(base, path, key string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}

	labels := strings.SplitN(path, ".", 3)
	if len(labels) == 3 {
		path = labels[0] + "." + labels[1]
	}
	return u.JoinPath(path + "." + key).String(), nil
}

func (p *Provider) fetchOne(ctx context.Context, sourcePath, key string) (*analysis.ConfigurationEntry, *analysis.MissingContext) {

	reqURL, err := buildConfigURL(p.baseURL, sourcePath, key)
	if err != nil {
		return nil, missingFor(sourcePath, key, analysis.ReasonRequestFailed)
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, missingFor(sourcePath, key, analysis.ReasonRequestFailed)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, missingFor(sourcePath, key, analysis.ReasonTimeout)
		}
		return nil, missingFor(sourcePath, key, analysis.ReasonRequestFailed)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, missingFor(sourcePath, key, analysis.ReasonTimeout)
		}
		return nil, missingFor(sourcePath, key, analysis.ReasonRequestFailed)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, missingFor(sourcePath, key, analysis.ReasonHTTPStatus)
	}
	if len(bytes.TrimSpace(body)) == 0 {

		return nil, missingFor(sourcePath, key, analysis.ReasonEmptyBody)
	}

	value, err := decodeCurrentValue(body)
	if err != nil {
		return nil, missingFor(sourcePath, key, analysis.ReasonInvalidJSON)
	}

	return &analysis.ConfigurationEntry{
		Key:    key,
		Value:  value,
		ReadAt: p.clock.Now().UTC(),
	}, nil
}

func decodeCurrentValue(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected trailing content after the configuration descriptor")
	}
	v, ok := obj[currentValueField]
	if !ok {
		return nil, errors.New("configuration descriptor is missing the currentValue field")
	}
	return v, nil
}

func missingFor(sourcePath, key, reason string) *analysis.MissingContext {
	return &analysis.MissingContext{
		Provider: analysis.ProviderConfiguration,
		Entity:   sourcePath,
		Key:      key,
		Reason:   reason,
	}
}

var _ contextbuilder.ConfigurationProvider = (*Provider)(nil)
