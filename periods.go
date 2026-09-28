package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Period struct {
	ChannelID int64  `json:"channel_id"`
	Month     string `json:"month"`
	Start     string `json:"start"`
	End       string `json:"end"`
}
type PeriodStore struct {
	mu    sync.RWMutex
	path  string
	items []Period
}

func dateBounds(start, end string, loc *time.Location) (time.Time, time.Time, error) {
	a, e1 := time.ParseInLocation("2006-01-02", start, loc)
	b, e2 := time.ParseInLocation("2006-01-02", end, loc)
	if e1 != nil || e2 != nil || len(start) != 10 || len(end) != 10 || a.Year() < 2000 || b.Year() > 2100 || b.Before(a) {
		return a, b, fmt.Errorf("请填写有效起止日期，结束日期不得早于开始日期")
	}
	// Inclusive end date; AddDate respects daylight-saving transitions.
	b = b.AddDate(0, 0, 1)
	if b.After(a.AddDate(0, 0, 366)) {
		return a, b, fmt.Errorf("日期范围最多 366 天")
	}
	return a, b, nil
}
func validatePeriods(items []Period, loc *time.Location) error {
	for i, p := range items {
		if p.ChannelID < 1 {
			return fmt.Errorf("请选择渠道")
		}
		if _, _, err := dateBounds(p.Start, p.End, loc); err != nil {
			return err
		}
		if p.Month != p.Start[:7] {
			return fmt.Errorf("归属月份必须是开始日期所在月份")
		}
		for _, q := range items[:i] {
			if q.ChannelID == p.ChannelID && (q.Month == p.Month || (p.Start <= q.End && q.Start <= p.End)) {
				return fmt.Errorf("同一渠道的周期不能重叠，每个归属月份只能有一个周期")
			}
		}
	}
	return nil
}
func openPeriods(path string, loc *time.Location) (*PeriodStore, error) {
	p := &PeriodStore{path: path, items: []Period{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &p.items); err != nil {
		return nil, fmt.Errorf("invalid period settings: %w", err)
	}
	if err = validatePeriods(p.items, loc); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *PeriodStore) list() []Period {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Period{}, p.items...)
}
func (p *PeriodStore) update(item Period, remove bool, loc *time.Location) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if item.ChannelID < 1 {
		return fmt.Errorf("无效的渠道")
	}
	if _, _, err := monthBounds(item.Month, loc); err != nil {
		return err
	}
	next := []Period{}
	for _, x := range p.items {
		if x.ChannelID != item.ChannelID || x.Month != item.Month {
			next = append(next, x)
		}
	}
	if !remove {
		next = append(next, item)
	}
	if len(next) > 2000 {
		return fmt.Errorf("周期配置数量超出上限")
	}
	if err := validatePeriods(next, loc); err != nil {
		return err
	}
	sort.Slice(next, func(i, j int) bool {
		if next[i].ChannelID != next[j].ChannelID {
			return next[i].ChannelID < next[j].ChannelID
		}
		return next[i].Month < next[j].Month
	})
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p.path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(p.path), ".periods-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err = file.Write(b); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, p.path); err != nil {
		return err
	}
	p.items = next
	return nil
}
func (a *App) periodsAPI(w http.ResponseWriter, r *http.Request) {
	if !a.authed(r) {
		jsonOut(w, 401, map[string]string{"error": "请先登录"})
		return
	}
	if a.periods == nil {
		jsonOut(w, 503, map[string]string{"error": "周期配置未启用"})
		return
	}
	if r.Method == "GET" {
		jsonOut(w, 200, a.periods.list())
		return
	}
	var req struct {
		Period
		Delete bool `json:"delete"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		jsonOut(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	if !req.Delete {
		if _, _, err := dateBounds(req.Start, req.End, a.cfg.Loc); err != nil {
			jsonOut(w, 400, map[string]string{"error": err.Error()})
			return
		}
		req.Month = req.Start[:7]
		var exists int
		err := a.store.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM channels WHERE id=?", req.ChannelID).Scan(&exists)
		if err != nil || exists == 0 {
			jsonOut(w, 400, map[string]string{"error": "渠道不存在"})
			return
		}
	}
	if err := a.periods.update(req.Period, req.Delete, a.cfg.Loc); err != nil {
		jsonOut(w, 400, map[string]string{"error": err.Error()})
		return
	}
	jsonOut(w, 200, a.periods.list())
}
func (a *App) applyPeriods(f *Filter) error {
	if f.Mode == "range" || a.periods == nil {
		return nil
	}
	configured := map[int64]bool{}
	found := map[int64]bool{}
	for _, p := range a.periods.list() {
		if f.Channel > 0 && p.ChannelID != f.Channel {
			continue
		}
		configured[p.ChannelID] = true
		if p.Month == f.Month {
			f.Periods = append(f.Periods, p)
			found[p.ChannelID] = true
		}
	}
	for id := range configured {
		f.Configured = append(f.Configured, id)
		if !found[id] {
			f.Missing = append(f.Missing, id)
		}
	}
	sort.Slice(f.Configured, func(i, j int) bool { return f.Configured[i] < f.Configured[j] })
	sort.Slice(f.Missing, func(i, j int) bool { return f.Missing[i] < f.Missing[j] })
	if f.Channel > 0 && len(f.Missing) > 0 {
		return fmt.Errorf("该渠道尚未配置 %s 的订阅周期，请先配置或改用日期范围", f.Month)
	}
	return nil
}
