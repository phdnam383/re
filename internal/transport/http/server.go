package http

import (
	"context"
	"log/slog"
	"net/http"
	"re/internal/profilemanagement"
	"re/internal/rulemanagement"
	"time"
)

type RuleService interface {
	Create(context.Context, rulemanagement.CreateInput) (rulemanagement.Rule, error)
	Get(context.Context, string) (rulemanagement.Rule, error)
	List(context.Context, rulemanagement.ListFilter) (rulemanagement.RulePage, error)
	Update(context.Context, string, rulemanagement.UpdateInput) (rulemanagement.Rule, error)
	Delete(context.Context, string) error
}

type handler struct {
	service RuleService
	log     *slog.Logger
}

func NewServer(addr string, service RuleService, profiles ProfileService, logger *slog.Logger) *http.Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	h := &handler{service: service, log: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/rules", h.create)
	mux.HandleFunc("GET /api/v1/rules", h.list)
	mux.HandleFunc("GET /api/v1/rules/{id}", h.get)
	mux.HandleFunc("PATCH /api/v1/rules/{id}", h.update)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", h.delete)

	ph := &profileHandler{service: profiles, handler: h}
	mux.HandleFunc("POST /api/v1/context-profiles", ph.create)
	mux.HandleFunc("GET /api/v1/context-profiles", ph.list)
	mux.HandleFunc("GET /api/v1/context-profiles/{id}", ph.get)
	mux.HandleFunc("PATCH /api/v1/context-profiles/{id}", ph.update)
	mux.HandleFunc("DELETE /api/v1/context-profiles/{id}", ph.delete)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
}

type ProfileService interface {
	Create(context.Context, profilemanagement.CreateInput) (profilemanagement.Profile, error)
	Get(context.Context, string) (profilemanagement.Profile, error)
	List(context.Context, profilemanagement.ListFilter) (profilemanagement.ProfilePage, error)
	Update(context.Context, string, profilemanagement.UpdateInput) (profilemanagement.Profile, error)
	Delete(context.Context, string) error
}

type profileHandler struct {
	*handler
	service ProfileService
}
