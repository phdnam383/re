package contextbuilder

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeProfile(t *testing.T) {
	selector := `{
		"probable_causes": ["LINK_TO_PEER_SIPGW_DOWN"],
		"alert_types": ["COMMUNICATIONS_ALERT"],
		"additional_information": {"dst_path": ["ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1"]}
	}`
	providers := `{
		"vdu": ["ims.vdu_sb_sip_core"],
		"link": [{"src_path": "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1", "dst_path": "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1"}],
		"configuration": [{"path": "ims.vdu_sb_logic.vnfc_sb_logic_1", "key": "number_of_log_file", "url": "http://api/v1/x"}]
	}`

	profile, err := DecodeProfile("sipgw", "desc", []byte(selector), []byte(providers))
	if err != nil {
		t.Fatalf("DecodeProfile() error = %v", err)
	}
	if profile.Name != "sipgw" || profile.Description != "desc" {
		t.Errorf("identity = %q/%q", profile.Name, profile.Description)
	}
	if got := profile.Selector.ProbableCauses; len(got) != 1 || got[0] != "LINK_TO_PEER_SIPGW_DOWN" {
		t.Errorf("probable causes = %v", got)
	}
	if len(profile.Providers.VDU) != 1 || len(profile.Providers.Link) != 1 || len(profile.Providers.Configuration) != 1 {
		t.Errorf("providers = %+v", profile.Providers)
	}
}

func TestDecodeProfileRejects(t *testing.T) {
	const okSelector = `{"probable_causes": ["THRESHOLD_CROSSING"]}`

	tests := []struct {
		name      string
		selector  string
		providers string
		wantField string
	}{
		{
			name:      "unknown provider key",
			selector:  okSelector,
			providers: `{"vdu": ["ims.vdu_a"], "health": ["ims.vdu_a"]}`,
			wantField: "providers",
		},
		{
			name:      "unknown selector key",
			selector:  `{"probable_cause": ["THRESHOLD_CROSSING"]}`,
			providers: `{}`,
			wantField: "selector",
		},
		{
			name:      "empty selector",
			selector:  `{}`,
			providers: `{"vdu": ["ims.vdu_a"]}`,
			wantField: "selector",
		},
		{
			name:      "selector with only empty clauses",
			selector:  `{"probable_causes": [], "alert_types": []}`,
			providers: `{}`,
			wantField: "selector",
		},
		{
			name:      "non-scalar selector value",
			selector:  `{"additional_information": {"peer": [{"path": "x"}]}}`,
			providers: `{}`,
			wantField: "selector.additional_information.peer[0]",
		},
		{
			name:      "invalid ltree label",
			selector:  okSelector,
			providers: `{"vdu": ["ims.vdu-a"]}`,
			wantField: "providers.vdu[0]",
		},
		{
			name:      "empty ltree label",
			selector:  okSelector,
			providers: `{"vdu": ["ims..vdu_a"]}`,
			wantField: "providers.vdu[0]",
		},
		{
			name:      "empty vdu path",
			selector:  okSelector,
			providers: `{"vdu": [""]}`,
			wantField: "providers.vdu[0]",
		},
		{
			name:      "link endpoint missing",
			selector:  okSelector,
			providers: `{"link": [{"src_path": "ims.vdu_a.vnfc_a_1", "dst_path": ""}]}`,
			wantField: "providers.link[0].dst_path",
		},
		{
			name:      "link to itself",
			selector:  okSelector,
			providers: `{"link": [{"src_path": "ims.vdu_a.vnfc_a_1", "dst_path": "ims.vdu_a.vnfc_a_1"}]}`,
			wantField: "providers.link[0]",
		},
		{
			name:      "configuration key empty",
			selector:  okSelector,
			providers: `{"configuration": [{"path": "ims.vdu_a.vnfc_a_1", "key": "  ", "url": "http://api/x"}]}`,
			wantField: "providers.configuration[0].key",
		},
		{
			name:      "configuration url is not http",
			selector:  okSelector,
			providers: `{"configuration": [{"path": "ims.vdu_a.vnfc_a_1", "key": "k", "url": "file:///etc/passwd"}]}`,
			wantField: "providers.configuration[0].url",
		},
		{
			name:      "configuration url has no host",
			selector:  okSelector,
			providers: `{"configuration": [{"path": "ims.vdu_a.vnfc_a_1", "key": "k", "url": "http:///v1/x"}]}`,
			wantField: "providers.configuration[0].url",
		},
		{
			name:      "trailing content after the document",
			selector:  okSelector,
			providers: `{"vdu": ["ims.vdu_a"]} {"vdu": ["ims.vdu_b"]}`,
			wantField: "providers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeProfile("p", "", []byte(tt.selector), []byte(tt.providers))
			var defErr *DefinitionError
			if !errors.As(err, &defErr) {
				t.Fatalf("error = %v, want *DefinitionError", err)
			}
			if defErr.Field != tt.wantField {
				t.Errorf("field = %q, want %q (%v)", defErr.Field, tt.wantField, err)
			}
			if defErr.Profile != "p" {
				t.Errorf("profile = %q, want %q", defErr.Profile, "p")
			}
		})
	}
}

func TestDecodeProfileRequiresName(t *testing.T) {
	_, err := DecodeProfile("  ", "", []byte(`{"probable_causes":["X"]}`), []byte(`{}`))
	var defErr *DefinitionError
	if !errors.As(err, &defErr) || defErr.Field != "name" {
		t.Fatalf("error = %v, want a name DefinitionError", err)
	}
}

// A profile may legitimately ask for nothing; the builder simply runs no
// provider for it.
func TestDecodeProfileAllowsEmptyProviders(t *testing.T) {
	if _, err := DecodeProfile("p", "", []byte(`{"alert_types":["EQUIPMENT_ALERT"]}`), nil); err != nil {
		t.Fatalf("DecodeProfile() error = %v", err)
	}
}

// The definitions shipped in context_profile/ must decode and validate. They
// are what the seed loads into context_profile, so a change that breaks them
// breaks the deployment, not just a fixture.
func TestShippedProfilesAreValid(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "context_profile", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no profile definitions found under context_profile/")
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			profile, err := loadProfileFile(t, file)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if profile.Selector.IsEmpty() {
				t.Error("selector is empty")
			}
		})
	}
}

// profileDocument is the on-disk shape of context_profile/*.json: the whole
// row, where the database stores selector and providers as separate columns.
type profileDocument struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Selector    json.RawMessage `json:"selector"`
	Providers   json.RawMessage `json:"providers"`
}

func loadProfileFile(t *testing.T, path string) (ContextProfile, error) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return ContextProfile{}, err
	}
	var doc profileDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ContextProfile{}, err
	}
	return DecodeProfile(doc.Name, doc.Description, doc.Selector, doc.Providers)
}

func mustLoadProfileFile(t *testing.T, name string) ContextProfile {
	t.Helper()
	profile, err := loadProfileFile(t, filepath.Join("..", "..", "context_profile", name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return profile
}

func TestDefinitionErrorMessage(t *testing.T) {
	err := &DefinitionError{Profile: "p", Field: "providers.vdu[0]", Detail: "boom"}
	if got := err.Error(); !strings.Contains(got, `"p"`) || !strings.Contains(got, "providers.vdu[0]") {
		t.Errorf("Error() = %q", got)
	}
	anon := &DefinitionError{Field: "name", Detail: "boom"}
	if got := anon.Error(); strings.Contains(got, `""`) {
		t.Errorf("Error() = %q, should omit an unknown profile", got)
	}
}
