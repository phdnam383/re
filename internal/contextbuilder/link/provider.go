package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"re/internal/analysis"
	"re/internal/contextbuilder"
)

const DefaultTimeout = 2 * time.Second

const requestTimeoutGrace = 500 * time.Millisecond

const maxBodyBytes = 1 << 20

const baseURL = "http://api/v1/probe/ping"

const respStatus = "status"

const linkCount = 3

type linkRequest struct {
	VNFCPath  string `json:"vnfcPath"`
	Target    string `json:"target"`
	Count     int    `json:"count"`
	TimeoutMs int    `json:"timeoutMs"`
}

type Options struct {
	Client *http.Client

	// Timeout is sent to the probe API as the timeout for one ping attempt.
	Timeout time.Duration

	// RequestTimeout bounds the complete HTTP exchange. When unset, it allows
	// all ping attempts plus a small amount of transport/encoding overhead.
	RequestTimeout time.Duration

	Clock contextbuilder.Clock

	BaseURL string

	Logger *slog.Logger
}

type Provider struct {
	client         *http.Client
	probeTimeout   time.Duration
	requestTimeout time.Duration
	clock          contextbuilder.Clock
	baseURL        string
	logger         *slog.Logger
}

func New(opts Options) *Provider {
	p := &Provider{
		client:         opts.Client,
		probeTimeout:   opts.Timeout,
		requestTimeout: opts.RequestTimeout,
		clock:          opts.Clock,
		baseURL:        opts.BaseURL,
		logger:         opts.Logger,
	}
	if p.client == nil {
		p.client = &http.Client{}
	}
	if p.probeTimeout <= 0 {
		p.probeTimeout = DefaultTimeout
	}
	if p.requestTimeout <= 0 {
		p.requestTimeout = time.Duration(linkCount)*p.probeTimeout + requestTimeoutGrace
	}
	if p.clock == nil {
		p.clock = contextbuilder.SystemClock()
	}
	if p.baseURL == "" {
		p.baseURL = baseURL
	}
	if p.logger == nil {
		p.logger = slog.New(slog.DiscardHandler)
	}
	return p
}

func (p *Provider) FetchLinks(ctx context.Context, vnfcPath string, targets []contextbuilder.LinkTarget) (contextbuilder.LinkResult, error) {
	if len(targets) == 0 {
		return contextbuilder.LinkResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return contextbuilder.LinkResult{}, err
	}

	entries := make([]*analysis.LinkEntry, len(targets))
	missing := make([]*analysis.MissingContext, len(targets))

	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, miss := p.fetchOne(ctx, vnfcPath, target)
			entries[i], missing[i] = entry, miss
		}()
	}
	wg.Wait()

	var result contextbuilder.LinkResult
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

func (p *Provider) fetchOne(ctx context.Context, vnfcPath string, t contextbuilder.LinkTarget) (*analysis.LinkEntry, *analysis.MissingContext) {

	callCtx, cancel := context.WithTimeout(ctx, p.requestTimeout)
	defer cancel()

	body, err := json.Marshal(linkRequest{
		VNFCPath:  vnfcPath,
		Target:    t.Target,
		Count:     linkCount,
		TimeoutMs: int(p.probeTimeout / time.Millisecond),
	})
	if err != nil {
		return p.failed(ctx, vnfcPath, t, analysis.ReasonRequestFailed, err)
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.baseURL, bytes.NewReader(body))
	if err != nil {
		return p.failed(ctx, vnfcPath, t, analysis.ReasonRequestFailed, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return p.failed(ctx, vnfcPath, t, analysis.ReasonTimeout, err)
		}
		return p.failed(ctx, vnfcPath, t, analysis.ReasonRequestFailed, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return p.failed(ctx, vnfcPath, t, analysis.ReasonTimeout, err)
		}
		return p.failed(ctx, vnfcPath, t, analysis.ReasonRequestFailed, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.failed(ctx, vnfcPath, t, analysis.ReasonHTTPStatus, nil,
			slog.Int("http_status", resp.StatusCode))
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return p.failed(ctx, vnfcPath, t, analysis.ReasonEmptyBody, nil)
	}

	status, err := parseStatus(respBody)
	if err != nil {
		return p.failed(ctx, vnfcPath, t, analysis.ReasonInvalidJSON, err)
	}

	return &analysis.LinkEntry{
		Target: t.Target,
		Status: status,
		ReadAt: p.clock.Now().UTC(),
	}, nil
}

func (p *Provider) failed(
	ctx context.Context,
	vnfcPath string,
	t contextbuilder.LinkTarget,
	reason string,
	err error,
	extra ...any,
) (*analysis.LinkEntry, *analysis.MissingContext) {
	attrs := []any{
		slog.String("provider", analysis.ProviderLink),
		slog.String("vnfc_path", vnfcPath),
		slog.String("target", t.Target),
		slog.String("reason", reason),
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	attrs = append(attrs, extra...)
	p.logger.WarnContext(ctx, "link provider failed", attrs...)
	return nil, missingFor(t, reason)
}

func parseStatus(body []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return "", err
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", errors.New("unexpected trailing content after the probe JSON")
	}
	status, ok := obj[respStatus].(string)
	if !ok || status == "" {
		return "", errors.New("probe response has no status")
	}
	return status, nil
}

func missingFor(t contextbuilder.LinkTarget, reason string) *analysis.MissingContext {
	return &analysis.MissingContext{
		Provider: analysis.ProviderLink,
		Entity:   t.Target,
		Reason:   reason,
	}
}

var _ contextbuilder.LinkProvider = (*Provider)(nil)
