package contextbuilder

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// ContextProfile is one context_profile row: which alerts it applies to, and
// which provider work it asks for.
//
// Name is the identity — context_profile.name is UNIQUE — and it is what the
// snapshot reports as a matched profile. There is no version and no bundle:
// unlike IAE, this engine reads whatever is enabled at request time.
type ContextProfile struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Selector    Selector     `json:"selector"`
	Providers   ProviderSpec `json:"providers"`
}

// Selector is the alert-matching predicate stored in context_profile.selector.
//
// A profile matches when a *single* alert satisfies every populated clause.
// The conjunction is per alert, never across the alert set: a selector naming
// a probable cause and an alert type means "an alert with both", not "one
// alert with the cause plus another with the type".
type Selector struct {
	// ProbableCauses and AlertTypes are ORed within themselves and ANDed with
	// each other. Comparison is case-insensitive — these are 3GPP enum names
	// and an operator typing "communications_alert" means the same thing.
	ProbableCauses []string `json:"probable_causes,omitempty"`
	AlertTypes     []string `json:"alert_types,omitempty"`

	// AdditionalInformation discriminates on the alert payload that actually
	// separates two incidents sharing a probable cause — e.g. only
	// THRESHOLD_CROSSING alerts whose "metric" is "overload_ram".
	//
	// AND across keys, OR within one key's values. Keys are compared exactly
	// (JSON object keys are case-sensitive; the value vocabulary is not).
	// An empty value list asserts only that the key is present, which is how a
	// profile says "any alert carrying a dst_path" without enumerating them.
	AdditionalInformation map[string][]any `json:"additional_information,omitempty"`
}

// ProviderSpec is context_profile.providers: the work the profile asks for.
// Every target is a named path — there are no selectors, wildcards or hop
// counts here, so one row can be read to know what a request will fetch. A path
// stands for its subtree, which is what lets a profile name a VDU and get its
// instances without knowing how many there are; it never means "follow the
// topology from here".
type ProviderSpec struct {
	// VDU holds exact VDU paths. Each one also yields every VNFC in its
	// subtree; the profile does not name VNFCs individually.
	VDU []string `json:"vdu,omitempty"`

	Link          []LinkTarget          `json:"link,omitempty"`
	Configuration []ConfigurationTarget `json:"configuration,omitempty"`
}

// LinkTarget is one directed pair to fetch. Direction is significant: the
// provider looks up pairs in this direction and never the reverse.
//
// Each endpoint is a subtree root, not an exact address. Naming a VDU fetches
// every link between its instances and the other side's, which is what makes a
// profile survive scaling: the set of instance pairs changes every time a VDU
// grows or shrinks, and a profile that named them would stop fetching the
// connectivity of whichever instances came later. Naming a VNFC still fetches
// exactly that one instance's links, because a path is a descendant of itself.
type LinkTarget struct {
	SrcPath string `json:"src_path"`
	DstPath string `json:"dst_path"`
}

// Entity renders the pair as it appears in MissingContext.Entity. A link has
// no path of its own, so the two endpoints and the direction between them are
// its identity.
func (l LinkTarget) Entity() string { return l.SrcPath + "->" + l.DstPath }

// Matches reports whether a link row satisfies this target.
//
// It is the Go statement of the provider's SQL predicate, and the two have to
// agree: the provider decides what to fetch with ltree, and the builder decides
// what is missing with this. A target that matched no row is a gap; one row is
// enough to satisfy it, however many instance pairs it covers.
func (l LinkTarget) Matches(srcPath, dstPath string) bool {
	return underOrEqual(srcPath, l.SrcPath) && underOrEqual(dstPath, l.DstPath)
}

// underOrEqual mirrors the ltree <@ operator: a path is a descendant of itself,
// and the comparison is on whole labels — the trailing dot is what stops
// "ims.vdu_sb" from claiming "ims.vdu_sb_logic".
func underOrEqual(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+".")
}

// ConfigurationTarget is one effective-configuration read.
//
// The URL is carried by the profile rather than derived from path and key
// because the engine has no convention to derive it from; the NF configuration
// API is an external contract. The consequence is that (path, key) must resolve
// to one URL across every matched profile — see mergeConfiguration.
type ConfigurationTarget struct {
	Path string `json:"path"`
	Key  string `json:"key"`
	URL  string `json:"url"`
}

// --- decoding ------------------------------------------------------------

// DecodeProfile builds a ContextProfile from one context_profile row and
// validates it.
//
// Decoding is strict: an unrecognised key anywhere in either JSONB document is
// a DefinitionError. That is what turns "unknown provider" into a startup-time
// failure, and it is the only defence against a mistyped selector clause — a
// row saying "probable_cause" instead of "probable_causes" would otherwise
// silently drop the clause and widen the profile to everything the remaining
// clauses allow.
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

// decodeStrict rejects unknown fields and trailing content. An empty or NULL
// document decodes to the zero value; Validate is what decides whether that is
// acceptable (it is for providers, not for a selector).
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

// --- validation ----------------------------------------------------------

// Validate checks everything that must hold before any provider is called: a
// selector that can actually discriminate, well-formed ltree paths, link
// endpoints, configuration keys and http(s) URLs.
//
// It runs at decode time and again on every matched profile in resolve, so the
// invariant holds regardless of how a profile reached the builder — a test
// fixture built as a struct literal is checked exactly like a database row.
func (p ContextProfile) Validate() error {
	if err := p.Selector.validate(p.Name); err != nil {
		return err
	}
	return p.Providers.validate(p.Name)
}

func (s Selector) validate(profile string) error {
	if s.IsEmpty() {
		// A selector with no populated clause matches every alert, so the
		// profile would apply to every request and its provider work would be
		// fetched for incidents nobody related it to. If that is genuinely
		// wanted it has to be said explicitly with a clause, not by omission.
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

	for key, values := range s.AdditionalInformation {
		if strings.TrimSpace(key) == "" {
			return definitionErrorf(profile, "selector.additional_information", "key must not be empty")
		}
		for i, v := range values {
			// Only JSON scalars are comparable (see matchesValue). An object or
			// array here could never match anything, and a profile that can
			// never match is a mistake worth failing on rather than a profile
			// that quietly never fires.
			if !isScalar(v) {
				return definitionErrorf(profile,
					fmtIndex("selector.additional_information."+key, i),
					"must be a JSON string, number, boolean or null")
			}
		}
	}
	return nil
}

// IsEmpty reports whether the selector declares no clause at all. An
// additional_information key with an empty value list *is* a clause — it
// asserts the key is present.
func (s Selector) IsEmpty() bool {
	return len(s.ProbableCauses) == 0 &&
		len(s.AlertTypes) == 0 &&
		len(s.AdditionalInformation) == 0
}

func (spec ProviderSpec) validate(profile string) error {
	// A profile asking for nothing is allowed: the builder skips providers
	// with no work, and a profile may exist to be matched and merged with
	// others before its own targets are filled in.
	for i, path := range spec.VDU {
		if err := validatePath(profile, fmtIndex("providers.vdu", i), path); err != nil {
			return err
		}
	}

	for i, l := range spec.Link {
		field := fmtIndex("providers.link", i)
		if err := validatePath(profile, field+".src_path", l.SrcPath); err != nil {
			return err
		}
		if err := validatePath(profile, field+".dst_path", l.DstPath); err != nil {
			return err
		}
		if l.SrcPath == l.DstPath {
			// The link table models a relationship between two VNFCs. A pair
			// pointing at itself can never be found, so it would show up as a
			// permanent NOT_FOUND that looks like missing topology data.
			return definitionErrorf(profile, field, "src_path and dst_path must differ")
		}
	}

	for i, c := range spec.Configuration {
		field := fmtIndex("providers.configuration", i)
		if err := validatePath(profile, field+".path", c.Path); err != nil {
			return err
		}
		if strings.TrimSpace(c.Key) == "" {
			return definitionErrorf(profile, field+".key", "must not be empty")
		}
		if err := validateURL(profile, field+".url", c.URL); err != nil {
			return err
		}
	}
	return nil
}

// validatePath checks an ltree literal against the label rules in
// db/schema.sql: dot-separated labels of [A-Za-z0-9_].
//
// This is syntax only. Whether the path exists is a runtime answer and belongs
// in a MissingContext, not in a definition error.
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

// validateURL requires an absolute http(s) URL with a host. Restricting the
// scheme keeps the provider to plain HTTP GETs — a file:// or similar target
// would make a stored definition able to name a local resource.
func validateURL(profile, field, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return definitionErrorf(profile, field, "must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return definitionErrorf(profile, field, "%q is not a valid URL: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return definitionErrorf(profile, field, "%q must use the http or https scheme", raw)
	}
	if u.Host == "" {
		return definitionErrorf(profile, field, "%q must name a host", raw)
	}
	return nil
}

func isScalar(v any) bool {
	switch v.(type) {
	case nil, string, float64, bool:
		return true
	default:
		// encoding/json decodes every number into float64 and everything else
		// into []any or map[string]any.
		return false
	}
}

func fmtIndex(field string, i int) string {
	return field + "[" + strconv.Itoa(i) + "]"
}
