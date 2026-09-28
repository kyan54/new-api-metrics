package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata"
)

//go:embed web/*
var assets embed.FS

type Config struct {
	Addr, DB, User, Password, Currency string
	Loc                                *time.Location
	Quota                              float64
	Secure                             bool
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func config() (Config, error) {
	c := Config{Addr: env("LISTEN_ADDR", ":8090"), DB: env("NEW_API_DB", "/data/one-api.db"), User: env("ADMIN_USER", "admin"), Password: os.Getenv("ADMIN_PASSWORD"), Currency: env("QUOTA_CURRENCY", "USD"), Secure: env("COOKIE_SECURE", "false") == "true"}
	if file := os.Getenv("ADMIN_PASSWORD_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return c, err
		}
		c.Password = strings.TrimRight(string(b), "\r\n")
	}
	if len(c.Password) < 12 {
		return c, fmt.Errorf("ADMIN_PASSWORD must contain at least 12 bytes")
	}
	var err error
	c.Loc, err = time.LoadLocation(env("REPORT_TIMEZONE", "Asia/Taipei"))
	if err != nil {
		return c, err
	}
	c.Quota, err = strconv.ParseFloat(env("QUOTA_PER_UNIT", "500000"), 64)
	if err != nil || c.Quota <= 0 || math.IsInf(c.Quota, 0) || math.IsNaN(c.Quota) {
		return c, fmt.Errorf("invalid QUOTA_PER_UNIT")
	}
	return c, nil
}

type attempt struct {
	Count int
	Start time.Time
}
type App struct {
	store    *Store
	cfg      Config
	secret   []byte
	mu       sync.Mutex
	attempts map[string]attempt
	queries  chan struct{}
	periods  *PeriodStore
}

func newApp(s *Store, c Config) *App {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &App{store: s, cfg: c, secret: key, attempts: map[string]attempt{}, queries: make(chan struct{}, 1)}
}
func (a *App) token(exp string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(exp))
	return exp + "." + hex.EncodeToString(m.Sum(nil))
}
func (a *App) authed(r *http.Request) bool {
	c, err := r.Cookie("metrics_session")
	if err != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	return err == nil && n > time.Now().Unix() && hmac.Equal([]byte(c.Value), []byte(a.token(parts[0])))
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	a.mu.Lock()
	for k, v := range a.attempts {
		if now.Sub(v.Start) > 5*time.Minute {
			delete(a.attempts, k)
		}
	}
	t := a.attempts[host]
	if t.Count >= 10 || len(a.attempts) >= 4096 {
		a.mu.Unlock()
		w.Header().Set("Retry-After", "300")
		jsonOut(w, 429, map[string]string{"error": "登录尝试过多，请五分钟后重试"})
		return
	}
	if t.Count == 0 {
		t.Start = now
	}
	t.Count++
	a.attempts[host] = t
	a.mu.Unlock()
	var form struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&form); err != nil {
		jsonOut(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	got := sha256.Sum256([]byte(form.Password))
	want := sha256.Sum256([]byte(a.cfg.Password))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 || form.Username != a.cfg.User {
		jsonOut(w, 401, map[string]string{"error": "用户名或密码错误"})
		return
	}
	a.mu.Lock()
	delete(a.attempts, host)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "metrics_session", Value: a.token(strconv.FormatInt(now.Add(8*time.Hour).Unix(), 10)), Path: "/", HttpOnly: true, Secure: a.cfg.Secure, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) data(w http.ResponseWriter, r *http.Request) {
	if !a.authed(r) {
		jsonOut(w, 401, map[string]string{"error": "请先登录"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	select {
	case a.queries <- struct{}{}:
		defer func() { <-a.queries }()
	default:
		jsonOut(w, 503, map[string]string{"error": "正在生成其他报表，请稍后重试"})
		return
	}
	if r.URL.Path == "/api/options" {
		v, err := a.store.options(ctx, a.cfg.Loc)
		if err != nil {
			log.Printf("options: %v", err)
			jsonOut(w, 503, map[string]string{"error": "无法读取数据，请检查数据库挂载和版本兼容性"})
			return
		}
		jsonOut(w, 200, v)
		return
	}
	f, err := parseFilter(r.URL.Query(), a.cfg.Loc)
	if err != nil {
		jsonOut(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if err = a.applyPeriods(&f); err != nil {
		jsonOut(w, 400, map[string]string{"error": err.Error()})
		return
	}
	v, err := a.store.report(ctx, f, a.cfg.Loc, a.cfg.Quota, a.cfg.Currency)
	if err != nil {
		log.Printf("report: %v", err)
		jsonOut(w, 503, map[string]string{"error": "查询失败或超时，请稍后重试并检查数据库状态"})
		return
	}
	if r.URL.Path == "/api/export" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=metrics-%s-%s.csv", v.Start+"_"+v.End, f.Dimension))
		w.Write([]byte("\xef\xbb\xbf"))
		out := csv.NewWriter(w)
		out.Write([]string{"模式", "归属月份", "范围开始", "范围结束", "渠道周期说明", "时区", "维度", "ID", "名称", "用户ID", "用户", "请求数", "输入Token", "输出Token", "总Token", "消费额度", "折算费用", "费用单位", "零Token记录数"})
		for _, x := range v.Rows {
			out.Write([]string{v.Mode, v.Month, v.Start, v.End, periodDescription(v), v.Timezone, v.Dimension, strconv.FormatInt(x.ID, 10), safeCell(x.Name), strconv.FormatInt(x.UserID, 10), safeCell(x.User), strconv.FormatInt(x.Requests, 10), strconv.FormatInt(x.Input, 10), strconv.FormatInt(x.Output, 10), strconv.FormatInt(x.Total, 10), strconv.FormatInt(x.Quota, 10), strconv.FormatFloat(float64(x.Quota)/v.QuotaPerUnit, 'f', 6, 64), safeCell(v.Currency), strconv.FormatInt(x.ZeroUsage, 10)})
		}
		out.Flush()
		return
	}
	jsonOut(w, 200, v)
}
func (a *App) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("POST /api/login", a.login)
	m.HandleFunc("GET /api/periods", a.periodsAPI)
	m.HandleFunc("POST /api/periods", a.periodsAPI)
	m.HandleFunc("POST /api/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "metrics_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.Secure, SameSite: http.SameSiteStrictMode})
		jsonOut(w, 200, map[string]bool{"ok": true})
	})
	for _, p := range []string{"/api/options", "/api/report", "/api/export"} {
		m.HandleFunc("GET "+p, a.data)
	}
	sub, _ := fs.Sub(assets, "web")
	m.Handle("GET /", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Method == "POST" && r.Header.Get("X-Metrics-Request") != "1" {
			jsonOut(w, 403, map[string]string{"error": "无效请求来源"})
			return
		}
		m.ServeHTTP(w, r)
	})
}
func main() {
	c, err := config()
	if err != nil {
		log.Fatal(err)
	}
	s, err := openStore(c.DB)
	if err != nil {
		log.Fatal(err)
	}
	defer s.db.Close()
	a := newApp(s, c)
	a.periods, err = openPeriods(env("PERIODS_FILE", "state/periods.json"), c.Loc)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: c.Addr, Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-done
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	log.Printf("new-api-metrics listening on %s; timezone=%s; source=read-only", c.Addr, c.Loc)
	if err = srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func periodDescription(r Report) string {
	if r.Mode == "range" {
		return "统一自定义日期范围"
	}
	parts := []string{"未配置订阅的渠道按自然月"}
	for _, p := range r.Periods {
		parts = append(parts, fmt.Sprintf("渠道#%d: %s 至 %s", p.ChannelID, p.Start, p.End))
	}
	for _, id := range r.Missing {
		parts = append(parts, fmt.Sprintf("渠道#%d: 该月份缺少周期，未计入", id))
	}
	return strings.Join(parts, "; ")
}
