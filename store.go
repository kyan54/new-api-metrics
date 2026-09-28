package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	channel string
}
type Filter struct {
	Mode, Start, End    string
	Periods             []Period
	Configured, Missing []int64
	Month               string
	Dimension           string
	Channel, User, Key  int64
	Model               string
}
type Row struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	UserID    int64  `json:"user_id,omitempty"`
	User      string `json:"user,omitempty"`
	Requests  int64  `json:"requests"`
	Input     int64  `json:"input_tokens"`
	Output    int64  `json:"output_tokens"`
	Total     int64  `json:"total_tokens"`
	Quota     int64  `json:"quota"`
	ZeroUsage int64  `json:"zero_usage_requests"`
}
type Day struct {
	Date     string `json:"date"`
	Requests int64  `json:"requests"`
	Input    int64  `json:"input_tokens"`
	Output   int64  `json:"output_tokens"`
	Total    int64  `json:"total_tokens"`
}
type Report struct {
	Mode         string   `json:"mode"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
	Periods      []Period `json:"periods"`
	Missing      []int64  `json:"missing_period_channels"`
	Month        string   `json:"month"`
	Timezone     string   `json:"timezone"`
	Dimension    string   `json:"dimension"`
	Rows         []Row    `json:"rows"`
	Days         []Day    `json:"days"`
	Summary      Row      `json:"summary"`
	QuotaPerUnit float64  `json:"quota_per_unit"`
	Currency     string   `json:"currency"`
	Generated    string   `json:"generated_at"`
}
type Option struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Options struct {
	Channels     []Option `json:"channels"`
	Users        []Option `json:"users"`
	Keys         []Option `json:"keys"`
	Models       []string `json:"models"`
	FirstMonth   string   `json:"first_month"`
	CurrentMonth string   `json:"current_month"`
	Timezone     string   `json:"timezone"`
}

func openStore(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(3000)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err = s.checkSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) checkSchema() error {
	required := map[string][]string{
		"logs":     {"id", "user_id", "created_at", "type", "username", "token_name", "token_id", "model_name", "prompt_tokens", "completion_tokens", "quota"},
		"channels": {"id", "name"}, "users": {"id", "username"}, "tokens": {"id", "name"},
	}
	for table, fields := range required {
		rs, err := s.db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		found := map[string]bool{}
		for rs.Next() {
			var cid, nn, pk int
			var name, typ string
			var def any
			if err := rs.Scan(&cid, &name, &typ, &nn, &def, &pk); err != nil {
				rs.Close()
				return err
			}
			found[name] = true
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return err
		}
		for _, f := range fields {
			if !found[f] {
				return fmt.Errorf("unsupported schema: missing %s.%s", table, f)
			}
		}
		if table == "logs" {
			if found["channel_id"] {
				s.channel = "channel_id"
			} else if found["channel"] {
				s.channel = "channel"
			} else {
				return fmt.Errorf("unsupported schema: missing logs channel ID")
			}
		}
	}
	return nil
}
func monthBounds(month string, loc *time.Location) (time.Time, time.Time, error) {
	start, err := time.ParseInLocation("2006-01", month, loc)
	if err != nil || len(month) != 7 || start.Year() < 2000 || start.Year() > 2100 {
		return time.Time{}, time.Time{}, fmt.Errorf("月份格式应为 YYYY-MM，年份范围 2000–2100")
	}
	return start, start.AddDate(0, 1, 0), nil
}
func parseFilter(v url.Values, loc *time.Location) (Filter, error) {
	f := Filter{Mode: v.Get("mode"), Start: v.Get("start"), End: v.Get("end"), Month: v.Get("month"), Dimension: v.Get("dimension"), Model: v.Get("model")}
	if f.Mode == "" {
		f.Mode = "month"
	}
	if f.Mode != "month" && f.Mode != "range" {
		return f, fmt.Errorf("无效的时间模式")
	}
	if f.Month == "" {
		f.Month = time.Now().In(loc).Format("2006-01")
	}
	if f.Dimension == "" {
		f.Dimension = "channel"
	}
	if f.Dimension != "channel" && f.Dimension != "key" && f.Dimension != "user" && f.Dimension != "model" {
		return f, fmt.Errorf("无效的统计维度")
	}
	for k, p := range map[string]*int64{"channel": &f.Channel, "user": &f.User, "key": &f.Key} {
		if v.Get(k) != "" {
			n, err := strconv.ParseInt(v.Get(k), 10, 64)
			if err != nil || n < 1 {
				return f, fmt.Errorf("无效的筛选 ID")
			}
			*p = n
		}
	}
	if len(f.Model) > 256 {
		return f, fmt.Errorf("模型名称过长")
	}
	if f.Mode == "range" {
		_, _, err := dateBounds(f.Start, f.End, loc)
		f.Month = ""
		return f, err
	}
	_, _, err := monthBounds(f.Month, loc)
	return f, err
}

const sums = `COUNT(*), COALESCE(SUM(l.prompt_tokens),0), COALESCE(SUM(l.completion_tokens),0), COALESCE(SUM(COALESCE(l.prompt_tokens,0)+COALESCE(l.completion_tokens,0)),0), COALESCE(SUM(l.quota),0), SUM(CASE WHEN COALESCE(l.prompt_tokens,0)=0 AND COALESCE(l.completion_tokens,0)=0 THEN 1 ELSE 0 END)`

func (s *Store) report(ctx context.Context, f Filter, loc *time.Location, quota float64, currency string) (Report, error) {
	r := Report{Month: f.Month, Timezone: loc.String(), Dimension: f.Dimension, Rows: []Row{}, Days: []Day{}, QuotaPerUnit: quota, Currency: currency, Generated: time.Now().UTC().Format(time.RFC3339)}
	start, end, err := monthBounds(f.Month, loc)
	if f.Mode == "range" {
		start, end, err = dateBounds(f.Start, f.End, loc)
	}
	if err != nil {
		return r, err
	}
	r.Mode = f.Mode
	if r.Mode == "" {
		r.Mode = "month"
	}
	r.Periods = append([]Period{}, f.Periods...)
	r.Missing = append([]int64{}, f.Missing...)
	base := "(l.created_at>=? AND l.created_at<?"
	args := []any{start.Unix(), end.Unix()}
	if len(f.Configured) > 0 {
		base += " AND COALESCE(l." + s.channel + ",0) NOT IN ("
		for i, id := range f.Configured {
			if i > 0 {
				base += ","
			}
			base += "?"
			args = append(args, id)
		}
		base += ")"
	}
	base += ")"
	for _, p := range f.Periods {
		ps, pe, e := dateBounds(p.Start, p.End, loc)
		if e != nil {
			return r, e
		}
		base += " OR (l." + s.channel + "=? AND l.created_at>=? AND l.created_at<?)"
		args = append(args, p.ChannelID, ps.Unix(), pe.Unix())
		if f.Channel > 0 {
			start, end = ps, pe
		} else {
			if ps.Before(start) {
				start = ps
			}
			if pe.After(end) {
				end = pe
			}
		}
	}
	r.Start = start.Format("2006-01-02")
	r.End = end.AddDate(0, 0, -1).Format("2006-01-02")
	where := "l.type=2 AND (" + base + ")"
	for _, x := range []struct {
		col string
		id  int64
	}{{s.channel, f.Channel}, {"user_id", f.User}, {"token_id", f.Key}} {
		if x.id > 0 {
			where += " AND l." + x.col + "=?"
			args = append(args, x.id)
		}
	}
	if f.Model != "" {
		where += " AND l.model_name=?"
		args = append(args, f.Model)
	}
	// Read both views in one short snapshot; no persistent transaction or schema writes.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	id := "COALESCE(l." + s.channel + ",0)"
	name := "COALESCE(NULLIF(MAX(c.name),''),'已删除渠道 #' || " + id + ")"
	userID := "0"
	userName := "''"
	group := id
	switch f.Dimension {
	case "model":
		id = "0"
		group = "COALESCE(l.model_name,'')"
		name = "COALESCE(NULLIF(l.model_name,''),'未记录模型')"
	case "user":
		id = "COALESCE(l.user_id,0)"
		group = id
		name = "COALESCE(NULLIF(MAX(u.username),''),NULLIF(MAX(l.username),''),'未知用户 #' || " + id + ")"
	case "key":
		id = "COALESCE(l.token_id,0)"
		group = id + ",COALESCE(l.user_id,0)"
		userID = "COALESCE(l.user_id,0)"
		userName = "COALESCE(NULLIF(MAX(u.username),''),NULLIF(MAX(l.username),''),'未知用户')"
		name = "COALESCE(NULLIF(MAX(t.name),''),NULLIF(MAX(l.token_name),''),'未知 Key #' || " + id + ")"
	}
	query := "SELECT " + id + "," + name + "," + userID + "," + userName + "," + sums + " FROM logs l LEFT JOIN channels c ON c.id=l." + s.channel + " LEFT JOIN users u ON u.id=l.user_id LEFT JOIN tokens t ON t.id=l.token_id WHERE " + where + " GROUP BY " + group + " ORDER BY 8 DESC,1,3"
	rs, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return r, err
	}
	for rs.Next() {
		var x Row
		if err = rs.Scan(&x.ID, &x.Name, &x.UserID, &x.User, &x.Requests, &x.Input, &x.Output, &x.Total, &x.Quota, &x.ZeroUsage); err != nil {
			rs.Close()
			return r, err
		}
		r.Rows = append(r.Rows, x)
		r.Summary.Requests += x.Requests
		r.Summary.Input += x.Input
		r.Summary.Output += x.Output
		r.Summary.Total += x.Total
		r.Summary.Quota += x.Quota
		r.Summary.ZeroUsage += x.ZeroUsage
	}
	err = rs.Err()
	rs.Close()
	if err != nil {
		return r, err
	}
	// Group in Go using IANA timezone, so DST and local month boundaries are exact.
	rs, err = tx.QueryContext(ctx, "SELECT l.created_at,COALESCE(l.prompt_tokens,0),COALESCE(l.completion_tokens,0) FROM logs l WHERE "+where, args...)
	if err != nil {
		return r, err
	}
	days := map[string]*Day{}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		days[date] = &Day{Date: date}
	}
	for rs.Next() {
		var sec, a, b int64
		if err = rs.Scan(&sec, &a, &b); err != nil {
			rs.Close()
			return r, err
		}
		date := time.Unix(sec, 0).In(loc).Format("2006-01-02")
		x := days[date]
		x.Requests++
		x.Input += a
		x.Output += b
		x.Total += a + b
	}
	err = rs.Err()
	rs.Close()
	if err != nil {
		return r, err
	}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		r.Days = append(r.Days, *days[d.Format("2006-01-02")])
	}
	return r, tx.Commit()
}
func (s *Store) options(ctx context.Context, loc *time.Location) (Options, error) {
	o := Options{Channels: []Option{}, Users: []Option{}, Keys: []Option{}, Models: []string{}, Timezone: loc.String(), CurrentMonth: time.Now().In(loc).Format("2006-01")}
	// Only names and IDs are selected; API secrets and password hashes never leave the source DB.
	queries := []struct {
		q   string
		dst *[]Option
	}{
		{"SELECT id,MAX(name) FROM (SELECT id,name FROM channels UNION ALL SELECT " + s.channel + ",'已删除渠道 #' || " + s.channel + " FROM logs WHERE type=2 AND " + s.channel + " NOT IN (SELECT id FROM channels)) GROUP BY id ORDER BY id", &o.Channels},
		{"SELECT id,MAX(name) FROM (SELECT id,username AS name FROM users UNION ALL SELECT user_id,username FROM logs WHERE type=2 AND user_id NOT IN (SELECT id FROM users)) GROUP BY id ORDER BY id", &o.Users},
		{"SELECT id,MAX(name) FROM (SELECT id,name FROM tokens UNION ALL SELECT token_id,token_name FROM logs WHERE type=2 AND token_id NOT IN (SELECT id FROM tokens)) GROUP BY id ORDER BY id", &o.Keys},
	}
	for _, q := range queries {
		rs, err := s.db.QueryContext(ctx, q.q)
		if err != nil {
			return o, err
		}
		for rs.Next() {
			var x Option
			var name sql.NullString
			if err = rs.Scan(&x.ID, &name); err != nil {
				rs.Close()
				return o, err
			}
			x.Name = name.String
			*q.dst = append(*q.dst, x)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return o, err
		}
	}
	rs, err := s.db.QueryContext(ctx, "SELECT DISTINCT model_name FROM logs WHERE type=2 AND model_name IS NOT NULL ORDER BY model_name")
	if err != nil {
		return o, err
	}
	for rs.Next() {
		var m string
		if err = rs.Scan(&m); err != nil {
			rs.Close()
			return o, err
		}
		o.Models = append(o.Models, m)
	}
	err = rs.Err()
	rs.Close()
	if err != nil {
		return o, err
	}
	var first sql.NullInt64
	if err = s.db.QueryRowContext(ctx, "SELECT MIN(created_at) FROM logs WHERE type=2").Scan(&first); err != nil {
		return o, err
	}
	if first.Valid {
		o.FirstMonth = time.Unix(first.Int64, 0).In(loc).Format("2006-01")
	}
	return o, nil
}
func safeCell(s string) string {
	if strings.ContainsAny(strings.TrimLeft(s, " \t\r\n")[:min(1, len(strings.TrimLeft(s, " \t\r\n")))], "=+-@") {
		return "'" + s
	}
	return s
}
