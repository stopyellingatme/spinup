package handlers

import (
	"context"
	"net/http"
	"strconv"

	"example.com/todo/api/internal/gen"
	"example.com/todo/api/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/db"
	"github.com/stopyellingatme/spinup/kit/x/live"
)

func init() {
	required := []string{
		"body",
	}
	known := []string{
		"body",
	}
	requiredFields["POST /api/notes"] = required
	knownFields["POST /api/notes"] = known
	requiredFields["PUT /api/notes/{id}"] = required
	knownFields["PUT /api/notes/{id}"] = known
}

// noteFromStore converts a store row to the API type.
func noteFromStore(row store.Note) gen.Note {
	item := gen.Note{
		Id:        row.ID,
		Body:      row.Body,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	return item
}

// noteParams validates an API input and converts it to store values.
func noteParams(in gen.NoteInput) (store.CreateNoteParams, error) {
	params := store.CreateNoteParams{
		Body: in.Body,
	}
	return params, nil
}

// ListNotes returns one page of notes in id order, and the total count.
func (s *Server) ListNotes(ctx context.Context, req gen.ListNotesRequestObject) (gen.ListNotesResponseObject, error) {
	user, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.ListNotes401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	limit, offset, err := page(req.Params.Limit, req.Params.Offset)
	if err != nil {
		return nil, err
	}
	rows, err := s.Queries.ListNotes(ctx, store.ListNotesParams{UserID: user.ID, RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, err
	}
	// Each row carries the total. A page past the last row has no row, so
	// it counts the rows itself.
	var total int64
	if len(rows) > 0 {
		total = rows[0].Total
	} else if offset > 0 {
		total, err = s.Queries.CountNotes(ctx, user.ID)
		if err != nil {
			return nil, err
		}
	}
	items := make([]gen.Note, 0, len(rows))
	for _, row := range rows {
		items = append(items, noteFromStore(row.Note))
	}
	return gen.ListNotes200JSONResponse{Items: items, Total: total}, nil
}

// CreateNote inserts a note.
func (s *Server) CreateNote(ctx context.Context, req gen.CreateNoteRequestObject) (gen.CreateNoteResponseObject, error) {
	user, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.CreateNote401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	params, err := noteParams(*req.Body)
	if err != nil {
		return nil, err
	}
	params.UserID = user.ID
	var item gen.Note
	var row store.Note
	err = db.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		row, err = s.Queries.WithTx(tx).CreateNote(ctx, params)
		if err != nil {
			return err
		}
		item = noteFromStore(row)
		return live.Publish(ctx, tx, live.OwnedKey("notes", user.ID), live.Upsert(strconv.FormatInt(row.ID, 10), item))
	})
	if err != nil {
		return nil, err
	}
	return gen.CreateNote201JSONResponse(item), nil
}

// GetNote returns one note.
func (s *Server) GetNote(ctx context.Context, req gen.GetNoteRequestObject) (gen.GetNoteResponseObject, error) {
	user, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.GetNote401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	row, err := s.Queries.GetNote(ctx, store.GetNoteParams{ID: req.Id, UserID: user.ID})
	if err != nil {
		return nil, err
	}
	return gen.GetNote200JSONResponse(noteFromStore(row)), nil
}

// UpdateNote replaces every writable field of a note.
func (s *Server) UpdateNote(ctx context.Context, req gen.UpdateNoteRequestObject) (gen.UpdateNoteResponseObject, error) {
	user, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.UpdateNote401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	params, err := noteParams(*req.Body)
	if err != nil {
		return nil, err
	}
	var item gen.Note
	var row store.Note
	err = db.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		row, err = s.Queries.WithTx(tx).UpdateNote(ctx, store.UpdateNoteParams{
			ID:     req.Id,
			UserID: user.ID,
			Body:   params.Body,
		})
		if err != nil {
			return err
		}
		item = noteFromStore(row)
		return live.Publish(ctx, tx, live.OwnedKey("notes", user.ID), live.Upsert(strconv.FormatInt(row.ID, 10), item))
	})
	if err != nil {
		return nil, err
	}
	return gen.UpdateNote200JSONResponse(item), nil
}

// DeleteNote deletes a note.
func (s *Server) DeleteNote(ctx context.Context, req gen.DeleteNoteRequestObject) (gen.DeleteNoteResponseObject, error) {
	user, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.DeleteNote401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	var deleted int64
	err := db.InTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		deleted, err = s.Queries.WithTx(tx).DeleteNote(ctx, store.DeleteNoteParams{ID: req.Id, UserID: user.ID})
		if err != nil {
			return err
		}
		if deleted > 0 {
			return live.Publish(ctx, tx, live.OwnedKey("notes", user.ID), live.Delete(strconv.FormatInt(req.Id, 10)))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if deleted == 0 {
		return nil, errNotFound
	}
	return gen.DeleteNote204Response{}, nil
}
