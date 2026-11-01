package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"example.com/todo/api/internal/gen"
	"example.com/todo/api/internal/roles"
	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// ListNoteHistory returns the events visible to the signed-in user.
func (s *Server) ListNoteHistory(ctx context.Context, req gen.ListNoteHistoryRequestObject) (gen.ListNoteHistoryResponseObject, error) {
	user, ok := auth.UserFrom(ctx)
	if !ok {
		return noteHistoryResponse{status: http.StatusUnauthorized, body: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusUnauthorized), Status: http.StatusUnauthorized, Detail: "A signed-in user is required. Try: sign in."}}, nil
	}
	if !user.HasRole(roles.Manager) {
		return noteHistoryResponse{status: http.StatusForbidden, body: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusForbidden), Status: http.StatusForbidden, Detail: "Your role cannot read history. Try: sign in with an allowed role."}}, nil
	}
	filter := audit.Filter{Table: "notes", RecordID: strconv.FormatInt(req.Id, 10), Limit: audit.DefaultLimit}
	filter.OwnerID = user.ID
	if req.Params.Limit != nil {
		filter.Limit = int(*req.Params.Limit)
	}
	if req.Params.Offset != nil {
		filter.Offset = int(*req.Params.Offset)
	}
	if req.Params.BeforeId != nil {
		filter.BeforeID = *req.Params.BeforeId
	}
	err := filter.Validate()
	if err == nil && (filter.Limit < 1 || (req.Params.BeforeId != nil && filter.BeforeID < 1)) {
		err = fmt.Errorf("invalid history page. Try: use a positive limit and cursor.")
	}
	if err != nil {
		return noteHistoryResponse{status: http.StatusUnprocessableEntity, body: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusUnprocessableEntity), Status: http.StatusUnprocessableEntity, Detail: err.Error()}}, nil
	}
	events, err := audit.List(ctx, s.DB, filter)
	if err != nil {
		slog.ErrorContext(ctx, "read resource history", "error", err)
		return noteHistoryResponse{status: http.StatusInternalServerError, body: httpx.Problem{Type: "about:blank", Title: http.StatusText(http.StatusInternalServerError), Status: http.StatusInternalServerError, Detail: "Cannot read history. Try: check the database schema and retry."}}, nil
	}
	if events == nil {
		events = []audit.Event{}
	}
	data, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}
	items := make([]gen.AuditEvent, 0, len(events))
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return noteHistoryResponse{status: http.StatusOK, body: gen.AuditEventList{Items: items}}, nil
}

// noteHistoryResponse adds the cache policy to every history response.
type noteHistoryResponse struct {
	status int
	body   any
}

// VisitListNoteHistoryResponse writes a history result or a problem.
func (r noteHistoryResponse) VisitListNoteHistoryResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	media := "application/json"
	if r.status != http.StatusOK {
		media = "application/problem+json"
	}
	w.Header().Set("Content-Type", media)
	w.WriteHeader(r.status)
	return json.NewEncoder(w).Encode(r.body)
}
