package agent

import (
	"fmt"
	"signalwatch/internal/document"
)

const paperPassageLimit = 1000

func paperEvidence(chunks []Citation) []Citation {
	out := []Citation{}
	for _, chunk := range chunks {
		for _, passage := range document.Passages(chunk.Quote, paperPassageLimit) {
			source := chunk
			source.ID = fmt.Sprintf("%s-s%d", chunk.ID, passage.Start)
			source.Quote = passage.Text
			out = append(out, source)
		}
	}
	return out
}

type evidencePassage struct {
	ID    string `json:"id"`
	Quote string `json:"quote"`
}

// Source metadata remains on the server. The model only needs selectable text
// and opaque IDs; citations later inherit the pinned document/version/page.
func evidencePassages(evidence []Citation) []evidencePassage {
	out := make([]evidencePassage, 0, len(evidence))
	for _, source := range evidence {
		out = append(out, evidencePassage{source.ID, source.Quote})
	}
	return out
}
