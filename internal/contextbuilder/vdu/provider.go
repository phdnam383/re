package vdu

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

const baseURL = "http://api/v1/vdus"

type Options struct {
	Client  *http.Client
	Timeout time.Duration
	BaseURL string
}

type Provider struct {
	client  *http.Client
	timeout time.Duration
	baseURL string
}

func New(opts Options) *Provider {
	p := &Provider{client: opts.Client, timeout: opts.Timeout, baseURL: opts.BaseURL}
	if p.client == nil {
		p.client = &http.Client{}
	}
	if p.timeout <= 0 {
		p.timeout = DefaultTimeout
	}
	if p.baseURL == "" {
		p.baseURL = baseURL
	}
	return p
}

type vduResponse struct {
	Path        string         `json:"path"`
	Name        string         `json:"name"`
	Type        string         `json:"type,omitempty"`
	Namespace   string         `json:"namespace,omitempty"`
	Workload    string         `json:"workload,omitempty"`
	Instances   int            `json:"instances"`
	Selector    string         `json:"selector,omitempty"`
	ConfdPath   string         `json:"confdPath,omitempty"`
	ConfdServer string         `json:"confdServer,omitempty"`
	UpdatedAt   string         `json:"updatedAt,omitempty"`
	VNFCs       []vnfcResponse `json:"vnfcs"`
}

type vnfcResponse struct {
	ID        string           `json:"id"`
	K8sUID    string           `json:"k8sUid,omitempty"`
	PodName   string           `json:"podName"`
	Status    string           `json:"status"`
	Networks  []map[string]any `json:"networks,omitempty"`
	CreatedAt string           `json:"createdAt,omitempty"`
	UpdatedAt string           `json:"updatedAt,omitempty"`
}

type fetchResult struct {
	vdu     *analysis.VDU
	vnfcs   []analysis.VNFC
	missing *analysis.MissingContext
}

func (p *Provider) FetchVDUs(ctx context.Context, paths []string) (contextbuilder.VDUResult, error) {
	if len(paths) == 0 {
		return contextbuilder.VDUResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return contextbuilder.VDUResult{}, err
	}

	results := make([]fetchResult, len(paths))

	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = p.fetchOne(ctx, path)
		}()
	}
	wg.Wait()

	var out contextbuilder.VDUResult
	for _, r := range results {
		if r.vdu != nil {
			out.VDUs = append(out.VDUs, *r.vdu)
		}
		out.VNFCs = append(out.VNFCs, r.vnfcs...)
		if r.missing != nil {
			out.Missing = append(out.Missing, *r.missing)
		}
	}
	return out, nil
}

func (p *Provider) fetchOne(ctx context.Context, path string) fetchResult {
	reqURL, err := buildVDUURL(p.baseURL, path)
	if err != nil {
		return fetchResult{missing: missingFor(path, analysis.ReasonRequestFailed)}
	}

	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fetchResult{missing: missingFor(path, analysis.ReasonRequestFailed)}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fetchResult{missing: missingFor(path, analysis.ReasonTimeout)}
		}
		return fetchResult{missing: missingFor(path, analysis.ReasonRequestFailed)}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fetchResult{missing: missingFor(path, analysis.ReasonTimeout)}
		}
		return fetchResult{missing: missingFor(path, analysis.ReasonRequestFailed)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fetchResult{missing: missingFor(path, analysis.ReasonHTTPStatus)}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return fetchResult{missing: missingFor(path, analysis.ReasonEmptyBody)}
	}

	dto, err := decodeVDU(body)
	if err != nil {
		return fetchResult{missing: missingFor(path, analysis.ReasonInvalidJSON)}
	}

	vdu, vnfcs := p.toAnalysis(path, dto)
	return fetchResult{vdu: vdu, vnfcs: vnfcs}
}

func (p *Provider) toAnalysis(vduPath string, dto vduResponse) (*analysis.VDU, []analysis.VNFC) {
	vdu := &analysis.VDU{
		Path:        dto.Path,
		Name:        dto.Name,
		Type:        dto.Type,
		Namespace:   dto.Namespace,
		Workload:    dto.Workload,
		Instances:   dto.Instances,
		Selector:    dto.Selector,
		ConfdPath:   dto.ConfdPath,
		ConfdServer: dto.ConfdServer,
		UpdatedAt:   parseTime(dto.UpdatedAt),
	}
	vnfcs := make([]analysis.VNFC, len(dto.VNFCs))
	for i, n := range dto.VNFCs {
		vnfcs[i] = analysis.VNFC{
			Path:      n.ID,
			VDUPath:   vduPath,
			K8sUID:    n.K8sUID,
			Name:      n.PodName,
			Status:    n.Status,
			Networks:  n.Networks,
			CreatedAt: parseTime(n.CreatedAt),
			UpdatedAt: parseTime(n.UpdatedAt),
		}
	}
	return vdu, vnfcs
}

func buildVDUURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return u.JoinPath(path).String(), nil
}

func decodeVDU(body []byte) (vduResponse, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var dto vduResponse
	if err := dec.Decode(&dto); err != nil {
		return vduResponse{}, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return vduResponse{}, errors.New("unexpected trailing content after the vdu descriptor")
	}
	return dto, nil
}

func parseTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func missingFor(path, reason string) *analysis.MissingContext {
	return &analysis.MissingContext{
		Provider: analysis.ProviderVDU,
		Entity:   path,
		Reason:   reason,
	}
}

var _ contextbuilder.VDUProvider = (*Provider)(nil)
