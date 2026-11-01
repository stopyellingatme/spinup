// Package pdf serves a PDF from a report sidecar (ADR 0026 decision 6.2).
//
// A handler calls the Render method of the generated client of the PDF service
// (reports.New returns it) and passes the raw response to [Serve]:
//
//	resp, err := client.Render(r.Context(), reports.RenderJSONRequestBody{Template: "work_order", Data: data})
//	if err != nil { ... }
//	if err := pdf.Serve(w, resp, "work-order-42.pdf"); err != nil { ... }
package pdf
