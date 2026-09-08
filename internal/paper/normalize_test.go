package paper

import (
	"errors"
	"testing"
	"time"
)

func TestNormalizeRecordProducesStableSinglePaperFields(t *testing.T) {
	published := time.Date(2026, 8, 1, 2, 3, 4, 0, time.FixedZone("test", 8*60*60))
	record, err := NormalizeRecord(Record{
		ArXivID: " 2608.00001 ",
		Title:   " A\n useful paper ", Abstract: " Useful\tabstract ",
		Authors:     []string{" Jane  Doe ", "Jane Doe"},
		Categories:  []string{" cs.AI ", "cs.AI", "cs.LG"},
		PublishedAt: published, ArXivUpdatedAt: published.Add(time.Hour),
		ArXivURL: "https://arxiv.org/abs/2608.00001",
		PDFURL:   "https://arxiv.org/pdf/2608.00001",
	})
	if err != nil {
		t.Fatalf("normalize record: %v", err)
	}
	if record.ArXivID != "2608.00001" || record.Title != "A useful paper" ||
		record.Abstract != "Useful abstract" {
		t.Fatalf("unexpected normalized fields: %+v", record)
	}
	if len(record.Authors) != 1 || len(record.Categories) != 2 {
		t.Fatalf("lists were not normalized: %+v", record)
	}
	if record.PublishedAt.Location() != time.UTC || record.ArXivUpdatedAt.Location() != time.UTC {
		t.Fatal("arxiv times must be normalized to UTC")
	}
}

func TestNormalizeRecordRejectsInvalidFields(t *testing.T) {
	now := time.Now().UTC()
	valid := Record{
		ArXivID: "2608.00001", Title: "title", Abstract: "abstract",
		Authors: []string{"Jane Doe"}, Categories: []string{"cs.AI"},
		PublishedAt: now, ArXivUpdatedAt: now,
		ArXivURL: "https://arxiv.org/abs/2608.00001",
		PDFURL:   "https://arxiv.org/pdf/2608.00001",
	}
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{name: "missing abstract", mutate: func(record *Record) { record.Abstract = "" }},
		{name: "missing author", mutate: func(record *Record) { record.Authors = nil }},
		{name: "foreign URL", mutate: func(record *Record) { record.ArXivURL = "https://example.test/abs/2608.00001" }},
		{name: "insecure PDF URL", mutate: func(record *Record) { record.PDFURL = "http://arxiv.org/pdf/2608.00001" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if _, err := NormalizeRecord(record); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("expected invalid record, got %v", err)
			}
		})
	}
}
