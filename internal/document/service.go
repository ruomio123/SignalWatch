package document

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

func (s *Service) Run(ctx context.Context) {
	owner := rand.Text()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		d, err := s.Store.Claim(ctx, owner)
		if err == nil {
			s.process(ctx, d)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) process(ctx context.Context, d Document) {
	work, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-t.C:
				if s.Store.Renew(work, d) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	source, err := s.Store.Source(work, d.PaperID)
	if err == nil && Identity(source) != d.ID {
		err = ErrUnavailable
	}
	var extracted Extracted
	var pages []string
	if err == nil {
		extracted, err = s.Extractor.Extract(work, source)
		pages = extracted.Pages
	}
	if err == nil && (len(pages) == 0 || len(pages) > 200 || len(strings.Join(pages, "")) > 4<<20) {
		err = ErrUnavailable
	}
	chunks := Split(d.ID, pages)
	if err == nil && len(chunks) == 0 {
		err = ErrUnavailable
	}
	if err == nil {
		current, e := s.Store.Source(work, d.PaperID)
		if e != nil || Identity(current) != d.ID {
			err = ErrUnavailable
		}
	}
	if err != nil {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		code := "document_extraction_failed"
		if work.Err() != nil {
			code = "document_timeout"
		}
		_ = s.Store.Fail(cleanup, d, code)
		return
	}
	h := sha256.Sum256([]byte(strings.Join(pages, "\f")))
	_ = s.Store.Complete(work, d, chunks, hex.EncodeToString(h[:]), extracted.SourceVersion)
}
