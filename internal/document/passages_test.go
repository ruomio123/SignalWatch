package document

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPassagesPreserveExactOffsetsAndAllText(t *testing.T) {
	for _, text := range []string{"", "Short abstract.", strings.Repeat("Words with footnote † and ligature ﬁ. ", 100), strings.Repeat("无空格全文", 600), strings.Repeat("x", 3001), strings.Repeat(" \n\t", 800)} {
		passages := Passages(text, 1000)
		var restored strings.Builder
		offset := 0
		for _, passage := range passages {
			if passage.Start != offset || passage.End-passage.Start != len(passage.Text) || len(passage.Text) > 1000 || !utf8.ValidString(passage.Text) || passage.Text != text[passage.Start:passage.End] {
				t.Fatalf("invalid range %+v", passage)
			}
			restored.WriteString(passage.Text)
			offset = passage.End
		}
		if restored.String() != text {
			t.Fatal("lost source bytes")
		}
	}
}
