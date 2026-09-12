package contextbuilder

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

type ContextProfile struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Selector    Selector     `json:"selector"`
	Providers   ProviderSpec `json:"providers"`
}

type Selector struct {
	ProbableCauses []string `json:"probable_causes,omitempty"`
	AlertTypes     []string `json:"alert_types,omitempty"`
	// SourcePaths contains VDU paths (<namespace>.<vdu>), matching the VDU and its VNFCs.
	SourcePaths []string `json:"source_paths,omitempty"`

	AdditionalInformation map[string][]any `json:"additional_information,omitempty"`
}

type ProviderSpec struct {
	VDU []string `json:"vdu,omitempty"`

	Configuration []string     `json:"configuration,omitempty"`
	Link          []LinkTarget `json:"link,omitempty"`
	Metric        []string     `json:"metric,omitempty"`
}

type LinkTarget struct {
	Target string `json:"target"`
}

func DecodeProfile(name, description string, selectorJSON, providersJSON []byte) (ContextProfile, error) {
	p := ContextProfile{Name: strings.TrimSpace(name), Description: description}
	if p.Name == "" {
		return ContextProfile{}, definitionErrorf("", "name", "must not be empty")
	}

	if err := decodeStrict(selectorJSON, &p.Selector); err != nil {
		return ContextProfile{}, definitionErrorf(p.Name, "selector", "%v", err)
	}
	if err := decodeStrict(providersJSON, &p.Providers); err != nil {
		return ContextProfile{}, definitionErrorf(p.Name, "providers", "%v", err)
	}

	if err := p.Validate(); err != nil {
		return ContextProfile{}, err
	}
	return p, nil
}

func decodeStrict(data []byte, v any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingJSON
	}
	return nil
}

var errTrailingJSON = errors.New("unexpected trailing content after the JSON value")

func (p ContextProfile) Validate() error {
	if err := p.Selector.validate(p.Name); err != nil {
		return err
	}
	return p.Providers.validate(p.Name)
}

func (s Selector) validate(profile string) error {
	if s.IsEmpty() {

		return definitionErrorf(profile, "selector", "must declare at least one clause; an empty selector matches every alert")
	}

	for i, c := range s.ProbableCauses {
		if strings.TrimSpace(c) == "" {
			return definitionErrorf(profile, fmtIndex("selector.probable_causes", i), "must not be empty")
		}
	}
	for i, t := range s.AlertTypes {
		if strings.TrimSpace(t) == "" {
			return definitionErrorf(profile, fmtIndex("selector.alert_types", i), "must not be empty")
		}
	}

	for i, path := range s.SourcePaths {
		field := fmtIndex("selector.source_paths", i)
		if err := validatePath(profile, field, path); err != nil {
			return err
		}
		if strings.Count(path, ".") != 1 {
			return definitionErrorf(profile, field, "must be a VDU path with exactly two labels, e.g. ims.vdu_sb_logic")
		}
	}

	for key, values := range s.AdditionalInformation {
		if strings.TrimSpace(key) == "" {
			return definitionErrorf(profile, "selector.additional_information", "key must not be empty")
		}
		for i, v := range values {

			if !isScalar(v) {
				return definitionErrorf(profile,
					fmtIndex("selector.additional_information."+key, i),
					"must be a JSON string, number, boolean or null")
			}
		}
	}
	return nil
}

func (s Selector) IsEmpty() bool {
	return len(s.ProbableCauses) == 0 &&
		len(s.AlertTypes) == 0 &&
		len(s.SourcePaths) == 0 &&
		len(s.AdditionalInformation) == 0
}

func (spec ProviderSpec) validate(profile string) error {

	for i, path := range spec.VDU {
		if err := validatePath(profile, fmtIndex("providers.vdu", i), path); err != nil {
			return err
		}
	}

	for i, key := range spec.Configuration {
		field := fmtIndex("providers.configuration", i)
		if strings.TrimSpace(key) == "" {
			return definitionErrorf(profile, field, "must not be empty")
		}
	}

	for i, p := range spec.Link {
		field := fmtIndex("providers.link", i)
		if strings.TrimSpace(p.Target) == "" {
			return definitionErrorf(profile, field+".target", "must not be empty")
		}
	}

	for i, name := range spec.Metric {
		if err := validatePath(profile, fmtIndex("providers.metric", i), name); err != nil {
			return err
		}
	}
	return nil
}

func validatePath(profile, field, path string) error {
	if path == "" {
		return definitionErrorf(profile, field, "must not be empty")
	}
	for _, label := range strings.Split(path, ".") {
		if label == "" {
			return definitionErrorf(profile, field, "%q is not a valid ltree path: empty label", path)
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			default:
				return definitionErrorf(profile, field,
					"%q is not a valid ltree path: label %q contains %q", path, label, r)
			}
		}
	}
	return nil
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, float64, bool:
		return true
	default:

		return false
	}
}

func fmtIndex(field string, i int) string {
	return field + "[" + strconv.Itoa(i) + "]"
}
