// Package cleaner — правила отбора «старых» и задублированных сеансов 1С.
//
// Логика намеренно вынесена отдельно от протокола и интерфейса: она не работает
// с сетью и полностью покрыта тестами. Смысл алгоритма:
//
//  1. служебные сеансы (регламентные задания, консоль сервера, COM-соединения)
//     и сеансы без имени пользователя не трогаем вообще;
//  2. остальных группируем по пользователю (по умолчанию — в разрезе
//     информационной базы, чтобы не «перебить» работу в другой базе);
//  3. внутри группы сортируем по последней активности и оставляем N самых
//     актуальных сеансов (по умолчанию один);
//  4. остальные считаем устаревшими дубликатами и помечаем на завершение,
//     если они прошли дополнительные проверки (простой, возраст).
package cleaner

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/config"
	"github.com/zigorminsk-debug/death-1c/internal/ras"
)

// Действия над сеансом.
const (
	ActionKeep      = "keep"
	ActionTerminate = "terminate"
	ActionSkip      = "skip"
)

// Item — одна строка таблицы сеансов вместе с решением программы.
type Item struct {
	UUID         string `json:"uuid"`
	ID           int    `json:"id"`
	UserName     string `json:"user_name"`
	AppID        string `json:"app_id"`
	Host         string `json:"host"`
	ClientIP     string `json:"client_ip"`
	InfobaseID   string `json:"infobase_id"`
	InfobaseName string `json:"infobase_name"`
	ClusterID    string `json:"cluster_id"`
	ClusterName  string `json:"cluster_name"`
	ProcessID    string `json:"process_id"`
	Locale       string `json:"locale"`

	StartedAt      string `json:"started_at"`
	LastActiveAt   string `json:"last_active_at"`
	StartedUnix    int64  `json:"started_unix"`
	LastActiveUnix int64  `json:"last_active_unix"`
	IdleMin        int    `json:"idle_min"`
	AgeMin         int    `json:"age_min"`

	Hibernate      bool    `json:"hibernate"`
	MemoryMB       float64 `json:"memory_mb"`
	CallsAll       int     `json:"calls_all"`
	CallsLast5Min  int64   `json:"calls_last5min"`
	Licenses       string  `json:"licenses"`
	DataSeparation string  `json:"data_separation"`
	CurrentService string  `json:"current_service"`

	Group       string `json:"group"`
	GroupTitle  string `json:"group_title"`
	GroupSize   int    `json:"group_size"`
	Rank        int    `json:"rank"`
	IsDuplicate bool   `json:"is_duplicate"`
	IsService   bool   `json:"is_service"`
	Action      string `json:"action"`
	Reason      string `json:"reason"`
}

// Summary — сводка по снимку сеансов.
type Summary struct {
	Total         int     `json:"total"`
	Users         int     `json:"users"`
	Groups        int     `json:"groups"`
	Duplicates    int     `json:"duplicates"`
	ToTerminate   int     `json:"to_terminate"`
	Service       int     `json:"service"`
	Kept          int     `json:"kept"`
	FreedMemoryMB float64 `json:"freed_memory_mb"`
	OldestIdleMin int     `json:"oldest_idle_min"`
}

// Plan — результат анализа: все сеансы с решениями и сводка.
type Plan struct {
	Items       []Item   `json:"items"`
	Summary     Summary  `json:"summary"`
	GeneratedAt string   `json:"generated_at"`
	Policy      string   `json:"policy"`
	Errors      []string `json:"errors,omitempty"`
}

// Targets возвращает сеансы, помеченные на завершение.
func (p *Plan) Targets() []Item {
	out := make([]Item, 0)
	for _, it := range p.Items {
		if it.Action == ActionTerminate {
			out = append(out, it)
		}
	}
	return out
}

// Build разбирает снимок сеансов и формирует план очистки.
func Build(sessions []ras.SessionView, policy config.Policy, now time.Time, softErrors []string) *Plan {
	plan := &Plan{
		Items:       make([]Item, 0, len(sessions)),
		GeneratedAt: now.Format(time.RFC3339),
		Policy:      describePolicy(policy),
		Errors:      softErrors,
	}

	excludedApps := lowerSet(policy.ExcludeAppIDs)
	protectedUsers := lowerSet(policy.ExcludeUsers)

	groups := map[string][]int{}
	groupTitles := map[string]string{}
	users := map[string]bool{}

	for i := range sessions {
		sv := sessions[i]
		it := toItem(sv, now)
		plan.Items = append(plan.Items, it)

		if sv.UserName != "" {
			users[sv.UserName] = true
		}

		switch {
		case excludedApps[strings.ToLower(strings.TrimSpace(sv.AppID))]:
			plan.Items[i].IsService = true
			plan.Items[i].Action = ActionSkip
			plan.Items[i].Reason = fmt.Sprintf("Служебный сеанс (%s) — программа его не завершает", appName(sv.AppID))
			continue
		case policy.SkipEmptyUser && strings.TrimSpace(sv.UserName) == "":
			plan.Items[i].IsService = true
			plan.Items[i].Action = ActionSkip
			plan.Items[i].Reason = "Сеанс без имени пользователя — программа его не завершает"
			continue
		case protectedUsers[strings.ToLower(strings.TrimSpace(sv.UserName))]:
			plan.Items[i].Action = ActionSkip
			plan.Items[i].Reason = "Пользователь в списке защищённых — сеанс не завершается"
			continue
		case sv.Session.StartedAt.IsZero() && sv.Session.LastActiveAt.IsZero():
			plan.Items[i].Action = ActionSkip
			plan.Items[i].Reason = "Сервер 1С не сообщил время сеанса — завершать такой сеанс небезопасно"
			continue
		}

		key := groupKey(sv, policy)
		if _, ok := groupTitles[key]; !ok {
			groupTitles[key] = groupTitle(sv, policy)
		}
		groups[key] = append(groups[key], i)
	}

	for key, idx := range groups {
		sortGroup(idx, plan.Items, policy)
		size := len(idx)
		for rank, pos := range idx {
			it := &plan.Items[pos]
			it.Group = key
			it.GroupTitle = groupTitles[key]
			it.GroupSize = size
			it.Rank = rank + 1
			it.IsDuplicate = size > 1 && rank > 0

			if rank < policy.KeepPerGroup {
				it.Action = ActionKeep
				it.Reason = keepReason(it, policy, size, rank)
				continue
			}

			if policy.MinIdleMin > 0 && it.IdleMin >= 0 && it.IdleMin < policy.MinIdleMin {
				it.Action = ActionKeep
				it.Reason = fmt.Sprintf("Дубликат, но простой %s меньше порога %d мин — оставлен по настройке",
					humanMin(it.IdleMin), policy.MinIdleMin)
				continue
			}
			if policy.MaxAgeHours > 0 && it.AgeMin >= 0 && it.AgeMin < policy.MaxAgeHours*60 {
				it.Action = ActionKeep
				it.Reason = fmt.Sprintf("Дубликат, но сеансу %s — меньше порога %d ч — оставлен по настройке",
					humanMin(it.AgeMin), policy.MaxAgeHours*60)
				continue
			}

			leader := plan.Items[idx[0]]
			it.Action = ActionTerminate
			it.Reason = fmt.Sprintf("Устаревший дубликат: у %s есть более актуальный сеанс (%s, активность %s назад)",
				displayUser(leader.UserName), leader.InfobaseName, humanMin(leader.IdleMin))
		}
	}

	plan.Summary = summarize(plan.Items, users)
	return plan
}

func toItem(sv ras.SessionView, now time.Time) Item {
	s := sv.Session
	it := Item{
		UUID:           s.UUID,
		ID:             s.ID,
		UserName:       s.UserName,
		AppID:          s.AppID,
		Host:           s.Host,
		ClientIP:       s.ClientIP,
		InfobaseID:     s.InfobaseID,
		InfobaseName:   sv.InfobaseName,
		ClusterID:      sv.ClusterID,
		ClusterName:    sv.ClusterName,
		ProcessID:      s.ProcessID,
		Locale:         s.Locale,
		Hibernate:      s.Hibernate,
		MemoryMB:       round1(float64(s.MemoryCurrent) / 1024 / 1024),
		CallsAll:       s.CallsAll,
		CallsLast5Min:  s.CallsLast5Min,
		DataSeparation: s.DataSeparation,
		CurrentService: s.CurrentService,
		Action:         ActionKeep,
		Reason:         "",
		IdleMin:        -1,
		AgeMin:         -1,
	}
	if !s.StartedAt.IsZero() {
		it.StartedAt = s.StartedAt.Format(time.RFC3339)
		it.StartedUnix = s.StartedAt.Unix()
		it.AgeMin = int(now.Sub(s.StartedAt).Minutes())
	}
	last := s.LastActivity()
	if !last.IsZero() {
		it.LastActiveAt = last.Format(time.RFC3339)
		it.LastActiveUnix = last.Unix()
		it.IdleMin = int(now.Sub(last).Minutes())
		if it.IdleMin < 0 {
			it.IdleMin = 0
		}
	}
	if len(s.Licenses) > 0 {
		parts := make([]string, 0, len(s.Licenses))
		for _, l := range s.Licenses {
			p := l.ShortPresent
			if p == "" {
				p = l.FullPresent
			}
			if p == "" {
				p = l.Series
			}
			if p != "" {
				parts = append(parts, p)
			}
		}
		it.Licenses = strings.Join(parts, "; ")
	}
	return it
}

func groupKey(sv ras.SessionView, policy config.Policy) string {
	user := strings.ToLower(strings.TrimSpace(sv.UserName))
	if policy.GroupBy == "user" {
		return "u:" + user + "@" + strings.ToLower(sv.ClusterID)
	}
	return "u:" + user + "@" + strings.ToLower(sv.ClusterID) + "@" + strings.ToLower(sv.InfobaseID)
}

func groupTitle(sv ras.SessionView, policy config.Policy) string {
	if policy.GroupBy == "user" {
		return displayUser(sv.UserName)
	}
	return displayUser(sv.UserName) + " · " + sv.InfobaseName
}

func sortGroup(idx []int, items []Item, policy config.Policy) {
	sort.SliceStable(idx, func(a, b int) bool {
		ia, ib := items[idx[a]], items[idx[b]]
		if policy.SortBy == "started" {
			if ia.StartedUnix != ib.StartedUnix {
				return ia.StartedUnix > ib.StartedUnix
			}
		} else {
			if ia.LastActiveUnix != ib.LastActiveUnix {
				return ia.LastActiveUnix > ib.LastActiveUnix
			}
			if ia.StartedUnix != ib.StartedUnix {
				return ia.StartedUnix > ib.StartedUnix
			}
		}
		if ia.ID != ib.ID {
			return ia.ID > ib.ID
		}
		return ia.UUID > ib.UUID
	})
}

func keepReason(it *Item, policy config.Policy, size, rank int) string {
	if size == 1 {
		return "Единственный сеанс пользователя — оставить"
	}
	if policy.KeepPerGroup > 1 {
		return fmt.Sprintf("Один из %d оставляемых актуальных сеансов пользователя (позиция %d)", policy.KeepPerGroup, rank+1)
	}
	return fmt.Sprintf("Самый актуальный сеанс пользователя: активность %s назад", humanMin(it.IdleMin))
}

func summarize(items []Item, users map[string]bool) Summary {
	var s Summary
	s.Total = len(items)
	s.Users = len(users)
	groups := map[string]bool{}
	for _, it := range items {
		switch it.Action {
		case ActionTerminate:
			s.ToTerminate++
			s.FreedMemoryMB += it.MemoryMB
			if it.IdleMin > s.OldestIdleMin {
				s.OldestIdleMin = it.IdleMin
			}
		case ActionSkip:
			s.Service++
		default:
			s.Kept++
		}
		if it.IsDuplicate {
			s.Duplicates++
		}
		if it.Group != "" {
			groups[it.Group] = true
		}
	}
	s.Groups = len(groups)
	s.FreedMemoryMB = round1(s.FreedMemoryMB)
	return s
}

func describePolicy(p config.Policy) string {
	scope := "пользователь + база"
	if p.GroupBy == "user" {
		scope = "пользователь (весь кластер)"
	}
	parts := []string{
		fmt.Sprintf("группировка: %s", scope),
		fmt.Sprintf("оставлять: %d", p.KeepPerGroup),
	}
	if p.MinIdleMin > 0 {
		parts = append(parts, fmt.Sprintf("простой от %d мин", p.MinIdleMin))
	}
	if p.MaxAgeHours > 0 {
		parts = append(parts, fmt.Sprintf("возраст от %d ч", p.MaxAgeHours))
	}
	if p.SortBy == "started" {
		parts = append(parts, "сортировка по запуску")
	} else {
		parts = append(parts, "сортировка по активности")
	}
	return strings.Join(parts, "; ")
}

func lowerSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, v := range list {
		v = strings.ToLower(strings.TrimSpace(v))
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func displayUser(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return "(без имени)"
	}
	return u
}

func appName(id string) string {
	switch id {
	case "BackgroundJob":
		return "регламентное задание"
	case "SrvrConsole":
		return "консоль администрирования"
	case "COMConnection":
		return "COM-соединение"
	case "Designer":
		return "Конфигуратор"
	case "WebServerExtension":
		return "веб-сервер"
	case "":
		return "неизвестное приложение"
	}
	return id
}

// humanMin превращает минуты в «2 ч 15 мин».
func humanMin(m int) string {
	if m < 0 {
		return "неизвестно"
	}
	if m == 0 {
		return "только что"
	}
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	h := m / 60
	rest := m % 60
	if rest == 0 {
		return fmt.Sprintf("%d ч", h)
	}
	return fmt.Sprintf("%d ч %d мин", h, rest)
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
