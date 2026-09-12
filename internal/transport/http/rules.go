package http

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"re/internal/rulemanagement"
	"strconv"
)

func decode(w http.ResponseWriter, r *http.Request) (ruleRequest, error) {
	var in ruleRequest
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return in, rulemanagement.Invalid("Content-Type", "must be application/json")
	}
	// Allow JSON escaping overhead while limiting the decoded GRL to 1 MiB.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return in, rulemanagement.Invalid("body", "cannot read body or body exceeds 8 MiB")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return in, rulemanagement.Invalid("body", "must be a JSON object")
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return in, rulemanagement.Invalid(name, "must not be null; omit unchanged fields")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, rulemanagement.Invalid("body", err.Error())
	}
	return in, nil
}
func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	in, err := decode(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	input := rulemanagement.CreateInput{Enabled: in.Enabled}
	if in.Name != nil {
		input.Name = *in.Name
	}
	if in.Description != nil {
		input.Description = *in.Description
	}
	if in.Content != nil {
		input.Content = *in.Content
	}
	if in.Salience != nil {
		input.Salience = *in.Salience
	}
	rule, err := h.service.Create(r.Context(), input)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/rules/"+rule.ID)
	writeJSON(w, http.StatusCreated, response(rule))
}
func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	rule, err := h.service.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response(rule))
}
func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	in, err := decode(w, r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rule, err := h.service.Update(r.Context(), r.PathValue("id"), rulemanagement.UpdateInput{Name: in.Name, Description: in.Description, Content: in.Content, Salience: in.Salience, Enabled: in.Enabled})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response(rule))
}
func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	filter := rulemanagement.ListFilter{Limit: 20}
	query := r.URL.Query()
	for key, values := range query {
		if len(values) != 1 {
			h.fail(w, r, rulemanagement.Invalid(key, "must occur once"))
			return
		}
		switch key {
		case "enabled":
			if values[0] != "true" && values[0] != "false" {
				h.fail(w, r, rulemanagement.Invalid(key, "must be true or false"))
				return
			}
			value := values[0] == "true"
			filter.Enabled = &value
		case "limit", "offset":
			value, err := strconv.Atoi(values[0])
			if err != nil || (key == "limit" && (value < 1 || value > 100)) || (key == "offset" && value < 0) {
				h.fail(w, r, rulemanagement.Invalid(key, "invalid pagination value"))
				return
			}
			if key == "limit" {
				filter.Limit = value
			} else {
				filter.Offset = value
			}
		default:
			h.fail(w, r, rulemanagement.Invalid(key, "unknown query parameter"))
			return
		}
	}
	page, err := h.service.List(r.Context(), filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]ruleResponse, 0, len(page.Items))
	for _, rule := range page.Items {
		items = append(items, response(rule))
	}
	writeJSON(w, http.StatusOK, struct {
		Items  []ruleResponse `json:"items"`
		Total  int64          `json:"total"`
		Limit  int            `json:"limit"`
		Offset int            `json:"offset"`
	}{items, page.Total, filter.Limit, filter.Offset})
}
