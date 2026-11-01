// Package pdf serves a PDF from a report sidecar (ADR 0026 decision 6.2).
//
// A handler calls the Render method of the generated client of the PDF service
// (reports.New returns it) and passes the raw response and the error to [Serve]:
//
//	resp, err := client.Render(r.Context(), reports.RenderJSONRequestBody{Template: "work_order", Data: data})
//	if err := pdf.Serve(w, resp, err, "work-order-42.pdf"); err != nil {
//		logger.WarnContext(r.Context(), "serve the report", "error", err)
//	}
//
// Serve answers the user with a problem that holds no sidecar text. The error
// that it returns holds the sidecar text for the log.
package pdf
