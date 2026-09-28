package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomRangeInclusive(t *testing.T) {
	s, db, _ := fixture(t, false)
	loc, _ := time.LoadLocation("Asia/Taipei")
	insert(t, db, "2026-01-28T15:59:59Z", 2, 1, 1, 1, 1000, 0, 0)
	insert(t, db, "2026-01-28T16:00:00Z", 2, 1, 1, 1, 10, 1, 0)
	insert(t, db, "2026-02-28T15:59:59Z", 2, 1, 1, 1, 20, 2, 0)
	insert(t, db, "2026-02-28T16:00:00Z", 2, 1, 1, 1, 1000, 0, 0)
	f, err := parseFilter(mustQuery("mode=range&start=2026-01-29&end=2026-02-28&dimension=key"), loc)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.report(context.Background(), f, loc, 500000, "USD")
	if err != nil || r.Summary.Total != 33 || r.Month != "" || r.Start != "2026-01-29" || r.End != "2026-02-28" || len(r.Days) != 31 {
		t.Fatal(r, err)
	}
	for _, q := range []string{"mode=range&start=2026-02-30&end=2026-03-01", "mode=range&start=2026-03-01&end=2026-02-01", "mode=range&start=2025-01-01&end=2026-03-01", "mode=range&start=2026-01-01", "mode=unknown"} {
		if _, err := parseFilter(mustQuery(q), loc); err == nil {
			t.Fatal(q)
		}
	}
}
func TestPeriodPersistenceAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "periods.json")
	p, err := openPeriods(path, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	one := Period{1, "2026-01", "2026-01-29", "2026-02-28"}
	if err = p.update(one, false, time.UTC); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Period{{1, "2026-02", "2026-02-28", "2026-03-28"}, {1, "2026-02", "2026-03-01", "2026-03-28"}, {0, "2026-01", "2026-01-01", "2026-01-30"}} {
		if err = p.update(bad, false, time.UTC); err == nil {
			t.Fatal("invalid period accepted", bad)
		}
	}
	again, err := openPeriods(path, time.UTC)
	if err != nil || len(again.list()) != 1 || again.list()[0] != one {
		t.Fatal(again, err)
	}
	if err = p.update(Period{1, "2026-01", "2026-01-05", "2026-02-04"}, false, time.UTC); err != nil {
		t.Fatal(err)
	}
	if len(p.list()) != 1 || p.list()[0].Start != "2026-01-05" {
		t.Fatal(p.list())
	}
	if err = p.update(Period{ChannelID: 1, Month: "2026-01"}, true, time.UTC); err != nil {
		t.Fatal(err)
	}
	again, err = openPeriods(path, time.UTC)
	if err != nil || len(again.list()) != 0 {
		t.Fatal(again, err)
	}
}
func TestSubscriptionScopes(t *testing.T) {
	s, db, _ := fixture(t, false)
	loc, _ := time.LoadLocation("Asia/Taipei")
	p, _ := openPeriods(filepath.Join(t.TempDir(), "periods.json"), loc)
	p.update(Period{1, "2026-01", "2026-01-29", "2026-02-28"}, false, loc)
	a := newApp(s, Config{Loc: loc})
	a.periods = p
	insert(t, db, "2026-01-05T00:00:00Z", 2, 1, 1, 1, 1000, 0, 0) // excluded from subscription
	insert(t, db, "2026-01-30T00:00:00Z", 2, 1, 1, 1, 10, 1, 0)
	insert(t, db, "2026-02-28T00:00:00Z", 2, 2, 2, 1, 20, 2, 0)   // belongs to January
	insert(t, db, "2026-01-05T00:00:00Z", 2, 1, 1, 9, 30, 3, 0)   // ordinary channel calendar
	insert(t, db, "2026-02-05T00:00:00Z", 2, 1, 1, 9, 2000, 0, 0) // excluded ordinary Feb
	for _, dim := range []string{"channel", "key", "user"} {
		f := Filter{Month: "2026-01", Dimension: dim}
		if err := a.applyPeriods(&f); err != nil {
			t.Fatal(err)
		}
		r, err := s.report(context.Background(), f, loc, 500000, "USD")
		if err != nil || r.Summary.Total != 66 || len(r.Periods) != 1 {
			t.Fatal(dim, r, err)
		}
	}
	f := Filter{Month: "2026-01", Dimension: "key", Channel: 1}
	a.applyPeriods(&f)
	r, err := s.report(context.Background(), f, loc, 500000, "USD")
	if err != nil || r.Start != "2026-01-29" || r.End != "2026-02-28" || r.Summary.Total != 33 {
		t.Fatal(r, err)
	}
	f = Filter{Month: "2026-02", Dimension: "channel", Channel: 1}
	if err = a.applyPeriods(&f); err == nil {
		t.Fatal("missing subscription must not fall back")
	}
	f.Channel = 0
	if err = a.applyPeriods(&f); err != nil {
		t.Fatal(err)
	}
	r, err = s.report(context.Background(), f, loc, 500000, "USD")
	if err != nil || r.Summary.Total != 2000 || len(r.Missing) == 0 {
		t.Fatal(r, err)
	}
	f = Filter{Mode: "range", Start: "2026-01-01", End: "2026-01-31", Dimension: "key", Channel: 1}
	a.applyPeriods(&f)
	r, err = s.report(context.Background(), f, loc, 500000, "USD")
	if err != nil || r.Summary.Total != 1011 || len(r.Periods) != 0 {
		t.Fatal(r, err)
	}
}
func TestPeriodAPIAuthAndMonthDerivation(t *testing.T) {
	s, _, _ := fixture(t, false)
	a := newApp(s, Config{Loc: time.UTC})
	a.periods, _ = openPeriods(filepath.Join(t.TempDir(), "periods.json"), time.UTC)
	h := a.handler()
	req := httptest.NewRequest("GET", "/api/periods", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	req = httptest.NewRequest("POST", "/api/periods", strings.NewReader(`{"channel_id":1,"month":"2026-02","start":"2026-01-29","end":"2026-02-28"}`))
	req.Header.Set("X-Metrics-Request", "1")
	req.Header.Set("Cookie", "metrics_session="+a.token("4102444800"))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var p []Period
	json.Unmarshal(w.Body.Bytes(), &p)
	if len(p) != 1 || p[0].Month != "2026-01" {
		t.Fatal(p)
	}
}
