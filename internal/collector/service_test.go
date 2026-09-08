package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"signalwatch/internal/paper"
	"signalwatch/internal/source"
	"signalwatch/internal/source/arxiv"
)

type demandStub struct {
	active []ActiveSource
	err    error
}

func (stub demandStub) ListActiveArXivSources(context.Context) ([]ActiveSource, error) {
	return stub.active, stub.err
}

type paperStub struct {
	calls     int
	sourceIDs []uint64
	records   [][]paper.Record
	errAtCall int
}

func (stub *paperStub) Upsert(
	_ context.Context,
	sourceID uint64,
	records []paper.Record,
	_ time.Time,
) (paper.UpsertResult, error) {
	stub.calls++
	stub.sourceIDs = append(stub.sourceIDs, sourceID)
	stub.records = append(stub.records, append([]paper.Record(nil), records...))
	if stub.errAtCall == stub.calls {
		return paper.UpsertResult{}, errors.New("paper store unavailable")
	}
	stored := make([]paper.Paper, len(records))
	for index := range records {
		stored[index].ID = uint64(stub.calls*100 + index + 1)
	}
	return paper.UpsertResult{Inserted: len(records), Papers: stored}, nil
}

type submitterStub struct {
	ids       []uint64
	errAtCall int
}

func (stub *submitterStub) Submit(_ context.Context, paperID uint64) error {
	stub.ids = append(stub.ids, paperID)
	if stub.errAtCall == len(stub.ids) {
		return errors.New("match queue unavailable")
	}
	return nil
}

type lockStub struct {
	acquired bool
}

func (stub lockStub) Acquire(context.Context, time.Duration) (ReleaseFunc, bool, error) {
	return func(context.Context) error { return nil }, stub.acquired, nil
}

type clientStub struct {
	requests []arxiv.FetchPageRequest
	fetch    func(int, arxiv.FetchPageRequest) (arxiv.FetchPageResult, error)
}

func (stub *clientStub) FetchPage(
	_ context.Context,
	request arxiv.FetchPageRequest,
) (arxiv.FetchPageResult, error) {
	call := len(stub.requests)
	stub.requests = append(stub.requests, request)
	if stub.fetch != nil {
		return stub.fetch(call, request)
	}
	return arxiv.FetchPageResult{
		Records:  []paper.Record{validRecordAt("2609.00001", time.Now().UTC())},
		Requests: 1, TotalResults: 1,
	}, nil
}

type factoryStub struct {
	client ArXivClient
	err    error
}

func (stub factoryStub) Create(string) (ArXivClient, error) {
	return stub.client, stub.err
}

func TestServiceFiltersInclusiveWindowAndStopsAfterOldRecord(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	windowStart := now.Add(-48 * time.Hour)
	endpoint := "https://export.arxiv.org/api/query"
	client := &clientStub{
		fetch: func(_ int, _ arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
			return arxiv.FetchPageResult{
				Records: []paper.Record{
					validRecordAt("future", now.Add(time.Nanosecond)),
					validRecordAt("upper-boundary", now),
					validRecordAt("inside", now.Add(-time.Hour)),
					validRecordAt("lower-boundary", windowStart),
					validRecordAt("too-old", windowStart.Add(-time.Nanosecond)),
				},
				Requests: 1, TotalResults: 1000,
			}, nil
		},
	}
	papers := &paperStub{}
	service := newTestService(t, demandStub{active: []ActiveSource{{
		Source:     source.Source{ID: 2, Kind: source.KindArXiv, Endpoint: &endpoint},
		Categories: []string{"cs.AI"},
	}}}, papers, lockStub{acquired: true}, factoryStub{client: client}, now, 10)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run collector: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("old record must stop later pages, requests=%d", len(client.requests))
	}
	if papers.calls != 1 || len(papers.records[0]) != 3 {
		t.Fatalf("expected three in-window records, calls=%d records=%+v", papers.calls, papers.records)
	}
	for index, expectedID := range []string{"upper-boundary", "inside", "lower-boundary"} {
		if papers.records[0][index].ArXivID != expectedID {
			t.Fatalf("unexpected retained records: %+v", papers.records[0])
		}
	}
}

func TestServicePersistsEachPageBeforeLaterFetchFailure(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	client := &clientStub{
		fetch: func(call int, _ arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
			if call == 2 {
				return arxiv.FetchPageResult{Requests: 2, TotalResults: 3}, errors.New("page unavailable")
			}
			return arxiv.FetchPageResult{
				Records:  []paper.Record{validRecordAt(string(rune('a'+call)), now.Add(-time.Hour))},
				Requests: 1, TotalResults: 3,
			}, nil
		},
	}
	papers := &paperStub{}
	service := newTestService(t, demandStub{active: []ActiveSource{{
		Source: source.Source{ID: 2, Endpoint: &endpoint}, Categories: []string{"cs.AI"},
	}}}, papers, lockStub{acquired: true}, factoryStub{client: client}, now, 4)

	if err := service.Run(context.Background()); err == nil {
		t.Fatal("expected later page failure")
	}
	if papers.calls != 2 || len(papers.records[0]) != 1 || len(papers.records[1]) != 1 {
		t.Fatalf("successful pages must remain persisted: calls=%d records=%+v", papers.calls, papers.records)
	}
	for index, expectedStart := range []int{0, 1, 2} {
		if client.requests[index].Start != expectedStart {
			t.Fatalf("unexpected page offsets: %+v", client.requests)
		}
	}
}

func TestServiceSubmitsEveryUpsertedPaperAfterEachPage(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	client := &clientStub{fetch: func(call int, _ arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		return arxiv.FetchPageResult{
			Records: []paper.Record{
				validRecordAt(string(rune('a'+call*2)), now),
				validRecordAt(string(rune('b'+call*2)), now),
			},
			Requests: 1, TotalResults: 4,
		}, nil
	}}
	papers := &paperStub{}
	submitter := &submitterStub{}
	service := newTestServiceWithSubmitter(t, demandStub{active: []ActiveSource{{
		Source: source.Source{ID: 2, Endpoint: &endpoint}, Categories: []string{"cs.AI"},
	}}}, papers, submitter, lockStub{acquired: true}, factoryStub{client: client}, now, 2)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run collector: %v", err)
	}
	want := []uint64{101, 102, 201, 202}
	if len(submitter.ids) != len(want) {
		t.Fatalf("expected all upserted papers submitted, got %v", submitter.ids)
	}
	for index := range want {
		if submitter.ids[index] != want[index] {
			t.Fatalf("unexpected submission order: got %v want %v", submitter.ids, want)
		}
	}
}

func TestServiceKeepsPersistedPageWhenSubmissionFails(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	papers := &paperStub{}
	submitter := &submitterStub{errAtCall: 1}
	client := &clientStub{fetch: func(int, arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
		return arxiv.FetchPageResult{
			Records:  []paper.Record{validRecordAt("2609.00003", now)},
			Requests: 1, TotalResults: 1,
		}, nil
	}}
	service := newTestServiceWithSubmitter(t, demandStub{active: []ActiveSource{{
		Source: source.Source{ID: 2, Endpoint: &endpoint}, Categories: []string{"cs.AI"},
	}}}, papers, submitter, lockStub{acquired: true}, factoryStub{client: client}, now, 2)

	if err := service.Run(context.Background()); err == nil {
		t.Fatal("expected submission failure")
	}
	if papers.calls != 1 || len(papers.records) != 1 {
		t.Fatalf("page must be persisted before queue submission: %+v", papers)
	}
}

func TestServiceCollectsEveryActiveCategorySequentially(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	client := &clientStub{
		fetch: func(call int, _ arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
			return arxiv.FetchPageResult{
				Records:  []paper.Record{validRecordAt(string(rune('a'+call)), now)},
				Requests: 1, TotalResults: 1,
			}, nil
		},
	}
	papers := &paperStub{}
	service := newTestService(t, demandStub{active: []ActiveSource{{
		Source:     source.Source{ID: 2, Kind: source.KindArXiv, Endpoint: &endpoint},
		Categories: []string{"cs.AI", "cs.CV"},
	}}}, papers, lockStub{acquired: true}, factoryStub{client: client}, now, 2)

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("run collector: %v", err)
	}
	if len(client.requests) != 2 || papers.calls != 2 ||
		client.requests[0].Category != "cs.AI" || client.requests[1].Category != "cs.CV" {
		t.Fatalf("expected sequential category collection: requests=%+v upserts=%d", client.requests, papers.calls)
	}
}

func TestServiceContinuesAfterOneCategoryFails(t *testing.T) {
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)
	endpoint := "https://export.arxiv.org/api/query"
	client := &clientStub{
		fetch: func(_ int, request arxiv.FetchPageRequest) (arxiv.FetchPageResult, error) {
			if request.Category == "cs.AI" {
				return arxiv.FetchPageResult{Requests: 1}, errors.New("category unavailable")
			}
			return arxiv.FetchPageResult{
				Records:  []paper.Record{validRecordAt("2609.00002", now)},
				Requests: 1, TotalResults: 1,
			}, nil
		},
	}
	papers := &paperStub{}
	service := newTestService(t, demandStub{active: []ActiveSource{{
		Source:     source.Source{ID: 2, Kind: source.KindArXiv, Endpoint: &endpoint},
		Categories: []string{"cs.AI", "cs.CV"},
	}}}, papers, lockStub{acquired: true}, factoryStub{client: client}, now, 2)

	if err := service.Run(context.Background()); err == nil {
		t.Fatal("cycle must report the failed category")
	}
	if len(client.requests) != 2 || papers.calls != 1 ||
		client.requests[1].Category != "cs.CV" {
		t.Fatalf("later category must still persist: requests=%+v upserts=%d", client.requests, papers.calls)
	}
}

func TestServiceSkipsWithoutDemandOrLock(t *testing.T) {
	for _, test := range []struct {
		name     string
		demands  []ActiveSource
		acquired bool
	}{
		{name: "no demand", acquired: true},
		{name: "another worker owns lock", demands: []ActiveSource{{}}, acquired: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			papers := &paperStub{}
			service := newTestService(
				t, demandStub{active: test.demands}, papers,
				lockStub{acquired: test.acquired}, factoryStub{client: &clientStub{}},
				time.Now().UTC(), 2,
			)
			if err := service.Run(context.Background()); err != nil {
				t.Fatalf("run collector: %v", err)
			}
			if papers.calls != 0 {
				t.Fatalf("collector must not write papers, got %d calls", papers.calls)
			}
		})
	}
}

func TestServiceReturnsCategoryFailure(t *testing.T) {
	now := time.Now().UTC()
	endpoint := "https://export.arxiv.org/api/query"
	service := newTestService(t, demandStub{active: []ActiveSource{{
		Source: source.Source{ID: 2, Endpoint: &endpoint}, Categories: []string{"cs.AI"},
	}}}, &paperStub{}, lockStub{acquired: true},
		factoryStub{err: errors.New("client unavailable")}, now, 2)
	if err := service.Run(context.Background()); err == nil {
		t.Fatal("expected collector failure")
	}
}

func newTestService(
	t *testing.T,
	demands DemandRepository,
	papers PaperRepository,
	lock LockManager,
	clients ClientFactory,
	now time.Time,
	maxPages int,
) *Service {
	return newTestServiceWithSubmitter(
		t, demands, papers, &submitterStub{}, lock, clients, now, maxPages,
	)
}

func newTestServiceWithSubmitter(
	t *testing.T,
	demands DemandRepository,
	papers PaperRepository,
	submitter PaperSubmitter,
	lock LockManager,
	clients ClientFactory,
	now time.Time,
	maxPages int,
) *Service {
	t.Helper()
	service, err := NewService(
		demands, papers, submitter, lock, clients,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now },
		Config{Lookback: 48 * time.Hour, LockTTL: 55 * time.Minute, MaxPages: maxPages},
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func validRecordAt(id string, updatedAt time.Time) paper.Record {
	return paper.Record{
		ArXivID: id, Title: "title", Abstract: "abstract",
		Authors: []string{"Author"}, Categories: []string{"cs.AI"},
		PublishedAt: updatedAt.Add(-time.Hour), ArXivUpdatedAt: updatedAt,
		ArXivURL: "https://arxiv.org/abs/" + id,
		PDFURL:   "https://arxiv.org/pdf/" + id,
	}
}
