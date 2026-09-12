package document

import "testing"

func TestSectionsPreserveOriginalLocationsAndUnknownText(t *testing.T) {
	pages := []string{"Title\n\n1 Introduction\nBody with no semantic heading.\n2.1 Experimental Setup\n", "LIMITATIONS\nText\n\nAppendix A"}
	sections := IdentifySections(pages)
	if len(sections) != 4 || sections[0].Page != 1 || sections[0].Line != 3 || sections[3].Page != 2 || sections[3].Line != 4 {
		t.Fatal(sections)
	}
	if got := IdentifySections([]string{"A sentence that is not a recognized heading."}); len(got) != 0 {
		t.Fatal(got)
	}
	chunks := Split("doc", pages)
	if len(chunks) != 2 {
		t.Fatal("section recognition must not replace page text")
	}
}
