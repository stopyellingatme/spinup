package live

import "strconv"

const (
	eventReady = "ready"
	eventPatch = "patch"
	eventSync  = "sync"
	eventBye   = "bye"
)

var pingFrame = []byte(": ping\n\n")

func patchFrame(epoch, seq int64, changes []byte) []byte {
	b := framePrefix(eventPatch, Cursor{epoch, seq}, true, len(changes)+128)
	b = append(b, `,"changes":`...)
	b = append(b, changes...)
	return append(b, "}\n\n"...)
}

func readyFrame(c Cursor, resumed bool) []byte {
	b := framePrefix(eventReady, c, true, 160)
	b = append(b, `,"resumed":`...)
	b = strconv.AppendBool(b, resumed)
	return append(b, "}\n\n"...)
}

func syncFrame(c Cursor, reason string) []byte {
	b := framePrefix(eventSync, c, false, 128)
	b = append(b, `,"reason":`...)
	b = strconv.AppendQuote(b, reason)
	return append(b, "}\n\n"...)
}

func byeFrame(reason string) []byte {
	b := make([]byte, 0, 64)
	b = append(b, "event: "...)
	b = append(b, eventBye...)
	b = append(b, "\ndata: {\"reason\":"...)
	b = strconv.AppendQuote(b, reason)
	return append(b, "}\n\n"...)
}

func framePrefix(event string, c Cursor, id bool, capacity int) []byte {
	b := make([]byte, 0, capacity)
	b = append(b, "event: "...)
	b = append(b, event...)
	if id {
		b = append(b, "\nid: "...)
		b = strconv.AppendInt(b, c.Epoch, 10)
		b = append(b, '-')
		b = strconv.AppendInt(b, c.Seq, 10)
	}
	b = append(b, "\ndata: {\"epoch\":"...)
	b = strconv.AppendInt(b, c.Epoch, 10)
	b = append(b, `,"seq":`...)
	return strconv.AppendInt(b, c.Seq, 10)
}
