package mcp

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

func (s *Server) metadata(origin string) http.HandlerFunc {
	title := s.cfg.Title
	if title == "" {
		title = s.cfg.Name
	}
	document := struct {
		Resource               string   `json:"resource"`
		ResourceName           string   `json:"resource_name"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}{origin + Path, title, []string{"header"}}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		// This document contains only strings, so encoding cannot fail.
		_ = httpx.WriteJSON(w, http.StatusOK, document)
	}
}
