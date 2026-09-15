package http

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"re/internal/profilemanagement"
	"strconv"
)

func decodeProfile(w http.ResponseWriter, r *http.Request) (profileRequest, error) {
	var in profileRequest
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return in, profilemanagement.Invalid("Content-Type", "must be application/json")
	}
	// Bound request size before decoding nested profile objects.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return in, profilemanagement.Invalid("body", "cannot read body or body exceeds 8 MiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return in, profilemanagement.Invalid("body", "must be a JSON object")
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return in, profilemanagement.Invalid(name, "must not be null; omit unchanged fields")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, profilemanagement.Invalid("body", err.Error())
	}
	return in, nil
}
func (h *profileHandler) create(w http.ResponseWriter, r *http.Request) {
	in, err := decodeProfile(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	input := profilemanagement.CreateInput{Enabled: in.Enabled}
	if in.Name != nil {
		input.Name = *in.Name
	}
	if in.Description != nil {
		input.Description = *in.Description
	}
	if in.Selector != nil {
		input.Selector = *in.Selector
	}
	if in.Providers != nil {
		input.Providers = *in.Providers
	}
	profile, err := h.service.Create(r.Context(), input)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/context-profiles/"+profile.ID)
	writeJSON(w, http.StatusCreated, profileResponseFrom(profile))
}
func (h *profileHandler) get(w http.ResponseWriter, r *http.Request) {
	profile, err := h.service.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profileResponseFrom(profile))
}
func (h *profileHandler) update(w http.ResponseWriter, r *http.Request) {
	in, err := decodeProfile(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	profile, err := h.service.Update(r.Context(), r.PathValue("id"), profilemanagement.UpdateInput{Name: in.Name, Description: in.Description, Selector: in.Selector, Providers: in.Providers, Enabled: in.Enabled})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profileResponseFrom(profile))
}
func (h *profileHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *profileHandler) list(w http.ResponseWriter, r *http.Request) {
	filter := profilemanagement.ListFilter{Limit: 20}
	query := r.URL.Query()
	for key, values := range query {
		if len(values) != 1 {
			h.fail(w, r, profilemanagement.Invalid(key, "must occur once"))
			return
		}
		switch key {
		case "enabled":
			if values[0] != "true" && values[0] != "false" {
				h.fail(w, r, profilemanagement.Invalid(key, "must be true or false"))
				return
			}
			value := values[0] == "true"
			filter.Enabled = &value
		case "limit", "offset":
			value, err := strconv.Atoi(values[0])
			if err != nil || (key == "limit" && (value < 1 || value > 100)) || (key == "offset" && value < 0) {
				h.fail(w, r, profilemanagement.Invalid(key, "invalid pagination value"))
				return
			}
			if key == "limit" {
				filter.Limit = value
			} else {
				filter.Offset = value
			}
		default:
			h.fail(w, r, profilemanagement.Invalid(key, "unknown query parameter"))
			return
		}
	}
	page, err := h.service.List(r.Context(), filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]profileResponse, 0, len(page.Items))
	for _, profile := range page.Items {
		items = append(items, profileResponseFrom(profile))
	}
	writeJSON(w, http.StatusOK, struct {
		Items  []profileResponse `json:"items"`
		Total  int64             `json:"total"`
		Limit  int               `json:"limit"`
		Offset int               `json:"offset"`
	}{items, page.Total, filter.Limit, filter.Offset})
}
