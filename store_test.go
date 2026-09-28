package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, wal bool) (*Store, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "one-api.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if wal {
		if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"CREATE TABLE channels(id INTEGER PRIMARY KEY,name TEXT,key TEXT)",
		"CREATE TABLE users(id INTEGER PRIMARY KEY,username TEXT,password TEXT)",
		"CREATE TABLE tokens(id INTEGER PRIMARY KEY,name TEXT,key TEXT)",
		"CREATE TABLE logs(id INTEGER PRIMARY KEY,user_id INTEGER,created_at INTEGER,type INTEGER,username TEXT,token_name TEXT,token_id INTEGER,model_name TEXT,prompt_tokens INTEGER,completion_tokens INTEGER,quota INTEGER,channel_id INTEGER)",
		"CREATE INDEX idx_created_at_type ON logs(created_at,type)",
		"INSERT INTO channels VALUES(1,'Agent Plan','upstream-secret')",
		"INSERT INTO users VALUES(1,'alice','password-secret'),(2,'bob','password-secret')",
		"INSERT INTO tokens VALUES(1,'same-name','key-secret-1'),(2,'same-name','key-secret-2')",
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close(); db.Close() })
	return s, db, path
}
func insert(t *testing.T, db *sql.DB, when string, typ, user, key, channel, in, out, quota int64) {
	t.Helper()
	d, err := time.Parse(time.RFC3339, when)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO logs(user_id,created_at,type,username,token_name,token_id,model_name,prompt_tokens,completion_tokens,quota,channel_id) VALUES(?,?,?,'old-user','old-key',?,'ark-code-latest',?,?,?,?)", user, d.Unix(), typ, key, in, out, quota, channel)
	if err != nil {
		t.Fatal(err)
	}
}
func TestMonthlyDimensions(t *testing.T) {
	s, db, _ := fixture(t, false)
	loc, _ := time.LoadLocation("Asia/Taipei")
	insert(t, db, "2026-08-31T15:59:59Z", 2, 1, 1, 1, 999, 999, 999)
	insert(t, db, "2026-08-31T16:00:00Z", 2, 1, 1, 1, 100, 10, 25)
	insert(t, db, "2026-09-30T15:59:59Z", 2, 2, 2, 9, 200, 20, 50)
	insert(t, db, "2026-09-30T16:00:00Z", 2, 1, 1, 1, 999, 999, 999)
	insert(t, db, "2026-09-10T00:00:00Z", 5, 1, 1, 1, 999, 999, 999)
	insert(t, db, "2026-09-10T00:00:00Z", 2, 1, 1, 1, 0, 0, 0)
	for _, dim := range []string{"channel", "key", "user"} {
		r, err := s.report(context.Background(), Filter{Month: "2026-09", Dimension: dim}, loc, 500000, "USD")
		if err != nil {
			t.Fatal(err)
		}
		if r.Summary.Total != 330 || r.Summary.Requests != 3 || r.Summary.Quota != 75 || r.Summary.ZeroUsage != 1 || len(r.Rows) != 2 {
			t.Fatalf("%s: %+v", dim, r)
		}
		var total int64
		for _, d := range r.Days {
			total += d.Total
		}
		if total != 330 || len(r.Days) != 30 {
			t.Fatal(r.Days)
		}
		if r.Rows[0].Total != 220 {
			t.Fatal("rows must sort by total tokens")
		}
	}
	r, err := s.report(context.Background(), Filter{Month: "2026-09", Dimension: "key", User: 1, Channel: 1, Key: 1, Model: "ark-code-latest"}, loc, 500000, "USD")
	if err != nil || r.Summary.Total != 110 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = s.report(context.Background(), Filter{Month: "2026-09", Dimension: "user", Model: "' OR 1=1 --"}, loc, 500000, "USD")
	if err != nil || r.Summary.Total != 0 {
		t.Fatal(r, err)
	}
	o, err := s.options(context.Background(), loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Channels) != 2 {
		t.Fatal(o)
	}
	b, _ := json.Marshal(o)
	if strings.Contains(string(b), "secret") {
		t.Fatal("secret leaked")
	}
}
func TestReadOnlyAndWAL(t *testing.T) {
	s, db, path := fixture(t, true)
	if _, err := s.db.Exec("DELETE FROM tokens"); err == nil {
		t.Fatal("write allowed")
	}
	insert(t, db, "2026-09-01T00:00:00Z", 2, 1, 1, 1, 12, 3, 0)
	r, err := s.report(context.Background(), Filter{Month: "2026-09", Dimension: "channel"}, time.UTC, 500000, "USD")
	if err != nil || r.Summary.Total != 15 {
		t.Fatal(r, err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
func TestMissingDBAndSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	s, err := openStore(path)
	if err == nil {
		s.db.Close()
		t.Fatal("missing DB accepted")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created source DB")
	}
	db, _ := sql.Open("sqlite", path)
	db.Exec("CREATE TABLE logs(id INTEGER)")
	db.Close()
	if s, err = openStore(path); err == nil {
		s.db.Close()
		t.Fatal("bad schema accepted")
	}
}
func TestDSTAndFilterValidation(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	a, b, err := monthBounds("2026-03", loc)
	if err != nil || b.Sub(a) != 743*time.Hour {
		t.Fatal(a, b, err)
	}
	for _, q := range []string{"month=2026-13", "month=2026-1", "dimension=sql", "key=-1", "user=abc"} {
		if _, err := parseFilter(mustQuery(q), loc); err == nil {
			t.Fatal(q)
		}
	}
}
func mustQuery(s string) url.Values { v, _ := url.ParseQuery(s); return v }
func TestHTTPAuthAndExport(t *testing.T) {
	s, db, _ := fixture(t, false)
	db.Exec("UPDATE tokens SET name='=HYPERLINK(test)' WHERE id=1")
	insert(t, db, "2026-09-01T00:00:00Z", 2, 1, 1, 1, 12, 3, 500000)
	a := newApp(s, Config{User: "admin", Password: "a-long-test-password", Loc: time.UTC, Quota: 500000, Currency: "USD", Secure: true})
	h := a.handler()
	req := httptest.NewRequest("GET", "/api/report", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	req = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"a-long-test-password"}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	req = httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"a-long-test-password"}`))
	req.Header.Set("X-Metrics-Request", "1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal(cookie)
	}
	for _, p := range []string{"/api/report?month=2026-09&dimension=key", "/api/options", "/api/export?month=2026-09&dimension=key"} {
		req = httptest.NewRequest("GET", p, nil)
		req.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("leak")
		}
		if strings.Contains(p, "export") && !strings.Contains(w.Body.String(), "'=HYPERLINK") {
			t.Fatal("unsafe CSV", w.Body.String())
		}
	}
	cookie.Value += "bad"
	req = httptest.NewRequest("GET", "/api/report", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("tampered cookie accepted")
	}
}
func TestLoginRateLimit(t *testing.T) {
	s, _, _ := fixture(t, false)
	a := newApp(s, Config{User: "admin", Password: "long-password", Loc: time.UTC})
	h := a.handler()
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
		r.Header.Set("X-Metrics-Request", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expected := 401
		if i == 10 {
			expected = 429
		}
		if w.Code != expected {
			t.Fatal(i, w.Code)
		}
	}
}

func TestModelAggregation(t *testing.T) {
	s, db, _ := fixture(t, false)
	insert(t, db, "2026-09-01T00:00:00Z", 2, 1, 1, 1, 100, 20, 40)
	insert(t, db, "2026-09-02T00:00:00Z", 2, 2, 2, 9, 200, 30, 60)
	insert(t, db, "2026-09-03T00:00:00Z", 2, 1, 1, 1, 300, 40, 80)
	db.Exec("UPDATE logs SET model_name='glm-5.3' WHERE id=3")
	f, err := parseFilter(mustQuery("dimension=model&month=2026-09"), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.report(context.Background(), f, time.UTC, 500000, "USD")
	if err != nil || len(r.Rows) != 2 || r.Summary.Total != 690 || r.Summary.Quota != 180 {
		t.Fatal(r, err)
	}
	names := map[string]Row{}
	for _, x := range r.Rows {
		names[x.Name] = x
	}
	if names["ark-code-latest"].Total != 350 || names["ark-code-latest"].Requests != 2 || names["glm-5.3"].Quota != 80 {
		t.Fatal(names)
	}
	f.Channel = 1
	r, err = s.report(context.Background(), f, time.UTC, 500000, "USD")
	if err != nil || r.Summary.Total != 460 {
		t.Fatal(r, err)
	}
	f.Model = "glm-5.3"
	r, err = s.report(context.Background(), f, time.UTC, 500000, "USD")
	if err != nil || len(r.Rows) != 1 || r.Summary.Total != 340 {
		t.Fatal(r, err)
	}
	if exportID("model", 0) != "" || exportID("user", 1) != "1" {
		t.Fatal("CSV IDs")
	}
}
