package contextbuilder

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"re/internal/analysis"
)

func TestConfigurationProfileKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		providers string
		wantErr   bool
	}{
		{"keys", `{"configuration":["log_file_count","log_level"]}`, false},
		{"empty list", `{"configuration":[]}`, false},
		{"empty key", `{"configuration":[""]}`, true},
		{"blank key", `{"configuration":["  "]}`, true},
		{"old object shape", `{"configuration":[{"path":"ims.logic","key":"log_level"}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeProfile("ram", "", []byte(`{"probable_causes":["OVERLOAD_RAM"]}`), []byte(tc.providers))
			if (err != nil) != tc.wantErr {
				t.Fatalf("DecodeProfile error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestResolveConfigurationKeys(t *testing.T) {
	profile := ContextProfile{
		Name:      "ram",
		Selector:  Selector{ProbableCauses: []string{"OVERLOAD_RAM"}},
		Providers: ProviderSpec{Configuration: []string{"log_level", "log_file_count", "log_level"}},
	}
	duplicate := profile
	duplicate.Name = "ram-extra"
	alerts := []analysis.Alert{{SourcePath: "ims.vdu_sb_logic.vnfc_sb_logic_1", ProbableCause: "OVERLOAD_RAM"}}
	plan, err := resolve(alerts, []ContextProfile{profile, duplicate})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"log_file_count", "log_level"}
	if !reflect.DeepEqual(plan.Configuration, want) {
		t.Fatalf("configuration keys = %+v, want %+v", plan.Configuration, want)
	}
}

func TestDecodeSourcePaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		valid bool
	}{
		{"VDU", []string{"ims.vdu_sb_logic"}, true},
		{"multiple VDUs", []string{"ims.vdu_sb_logic", "ims.vdu_sb_dns"}, true},
		{"uppercase", []string{"IMS.VDU_SB_LOGIC"}, true},
		{"empty selector", []string{}, false},
		{"blank path", []string{""}, false},
		{"whitespace", []string{" ims.vdu_sb_logic"}, false},
		{"root", []string{"ims"}, false},
		{"VNFC", []string{"ims.vdu_sb_logic.vnfc_1"}, false},
		{"wildcard", []string{"ims.vdu_sb_logic.*"}, false},
		{"empty label", []string{"ims."}, false},
		{"invalid character", []string{"ims.vdu-sb-logic"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"source_paths": tc.paths})
			if err != nil {
				t.Fatal(err)
			}
			p, err := DecodeProfile("source-only", "", data, []byte(`{}`))
			if (err == nil) != tc.valid {
				t.Fatalf("DecodeProfile error = %v, valid = %v", err, tc.valid)
			}
			if !tc.valid {
				var definition *DefinitionError
				if !errors.As(err, &definition) {
					t.Fatalf("expected DefinitionError, got %v", err)
				}
				field := "selector.source_paths[0]"
				if len(tc.paths) == 0 {
					field = "selector"
				}
				if definition.Field != field {
					t.Fatalf("error field = %q, want %q", definition.Field, field)
				}
				return
			}
			if p.Selector.IsEmpty() {
				t.Fatal("source_paths alone must count as a selector clause")
			}
			if _, err := resolve([]analysis.Alert{{SourcePath: "ims.vdu_sb_logic.vnfc_1"}}, []ContextProfile{p}); err != nil {
				t.Fatalf("resolve source-only profile: %v", err)
			}
		})
	}
}
