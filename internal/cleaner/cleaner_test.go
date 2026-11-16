package cleaner

import (
	"testing"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/config"
	"github.com/zigorminsk-debug/death-1c/internal/ras"
)

var now = time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

func sess(user, ib, ibName, app string, idleMin, ageMin int, id int) ras.SessionView {
	return ras.SessionView{
		Session: ras.Session{
			UUID:         "s-" + user + "-" + ib + "-" + time.Duration(idleMin).String(),
			ID:           id,
			AppID:        app,
			UserName:     user,
			Host:         "WS-" + user,
			InfobaseID:   ib,
			StartedAt:    now.Add(-time.Duration(ageMin) * time.Minute),
			LastActiveAt: now.Add(-time.Duration(idleMin) * time.Minute),
			MemoryCurrent: int64(id+1) * 1024 * 1024,
		},
		ClusterID:    "cluster-1",
		ClusterName:  "Кластер 1",
		InfobaseName: ibName,
	}
}

func findItem(t *testing.T, p *Plan, id int) Item {
	t.Helper()
	for _, it := range p.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("сеанс с ID %d не найден в плане", id)
	return Item{}
}

// Три сеанса одного пользователя в одной базе: два самых старых должны попасть
// под завершение, а остаться — самый активный.
func TestDuplicatesKeepMostActual(t *testing.T) {
	sessions := []ras.SessionView{
		sess("Иванова", "ib1", "Бухгалтерия", "1CV8C", 1450, 2900, 1),
		sess("Иванова", "ib1", "Бухгалтерия", "1CV8C", 4, 620, 2),
		sess("Иванова", "ib1", "Бухгалтерия", "1CV8C", 185, 1480, 3),
	}
	plan := Build(sessions, config.Default().Policy, now, nil)

	if plan.Summary.Total != 3 {
		t.Fatalf("всего сеансов: got %d, want 3", plan.Summary.Total)
	}
	if plan.Summary.ToTerminate != 2 {
		t.Fatalf("к завершению: got %d, want 2", plan.Summary.ToTerminate)
	}
	if got := findItem(t, plan, 2).Action; got != ActionKeep {
		t.Errorf("самый активный сеанс должен остаться, got %q", got)
	}
	for _, id := range []int{1, 3} {
		if got := findItem(t, plan, id).Action; got != ActionTerminate {
			t.Errorf("сеанс %d должен быть помечен на завершение, got %q", id, got)
		}
	}
	if !findItem(t, plan, 1).IsDuplicate {
		t.Error("устаревший сеанс должен быть помечен как дубликат")
	}
	if findItem(t, plan, 2).IsDuplicate {
		t.Error("актуальный сеанс не должен считаться дубликатом")
	}
}

// Один и тот же пользователь в разных базах — это разные группы:
// сеансы не должны «съедать» друг друга.
func TestDifferentInfobasesAreDifferentGroups(t *testing.T) {
	sessions := []ras.SessionView{
		sess("Петров", "ib1", "Бухгалтерия", "1CV8C", 5, 100, 1),
		sess("Петров", "ib2", "Зарплата", "1CV8C", 6, 100, 2),
	}
	plan := Build(sessions, config.Default().Policy, now, nil)
	if plan.Summary.ToTerminate != 0 {
		t.Fatalf("к завершению: got %d, want 0", plan.Summary.ToTerminate)
	}
	if plan.Summary.Groups != 2 {
		t.Fatalf("групп: got %d, want 2", plan.Summary.Groups)
	}
}

// При группировке «по пользователю» сеансы в разных базах считаются дубликатами.
func TestGroupByUserAcrossInfobases(t *testing.T) {
	policy := config.Default().Policy
	policy.GroupBy = "user"
	sessions := []ras.SessionView{
		sess("Петров", "ib1", "Бухгалтерия", "1CV8C", 5, 100, 1),
		sess("Петров", "ib2", "Зарплата", "1CV8C", 60, 300, 2),
	}
	plan := Build(sessions, policy, now, nil)
	if plan.Summary.ToTerminate != 1 {
		t.Fatalf("к завершению: got %d, want 1", plan.Summary.ToTerminate)
	}
	if got := findItem(t, plan, 2).Action; got != ActionTerminate {
		t.Errorf("менее активный сеанс должен завершаться, got %q", got)
	}
}

// Служебные сеансы и сеансы без имени пользователя не трогаем, даже если их много.
func TestServiceSessionsAreSkipped(t *testing.T) {
	sessions := []ras.SessionView{
		sess("", "ib1", "Бухгалтерия", "BackgroundJob", 1, 86400, 1),
		sess("", "ib1", "Бухгалтерия", "BackgroundJob", 2, 86400, 2),
		sess("Администратор", "ib1", "Бухгалтерия", "SrvrConsole", 300, 600, 3),
		sess("", "ib1", "Бухгалтерия", "1CV8C", 300, 600, 4),
	}
	plan := Build(sessions, config.Default().Policy, now, nil)
	if plan.Summary.ToTerminate != 0 {
		t.Fatalf("к завершению: got %d, want 0", plan.Summary.ToTerminate)
	}
	if plan.Summary.Service != 4 {
		t.Fatalf("служебных: got %d, want 4", plan.Summary.Service)
	}
	for _, it := range plan.Items {
		if it.Action != ActionSkip {
			t.Errorf("сеанс %d: action = %q, want skip", it.ID, it.Action)
		}
		if it.Reason == "" {
			t.Errorf("сеанс %d: нет пояснения", it.ID)
		}
	}
}

// Защищённые пользователи не завершаются никогда.
func TestProtectedUsers(t *testing.T) {
	policy := config.Default().Policy
	policy.ExcludeUsers = []string{"ночнойобмен"}
	sessions := []ras.SessionView{
		sess("НочнойОбмен", "ib1", "Бухгалтерия", "1CV8", 900, 4000, 1),
		sess("НочнойОбмен", "ib1", "Бухгалтерия", "1CV8", 30, 400, 2),
	}
	plan := Build(sessions, policy, now, nil)
	if plan.Summary.ToTerminate != 0 {
		t.Fatalf("к завершению: got %d, want 0", plan.Summary.ToTerminate)
	}
}

// Порог простоя защищает недавно активные дубликаты.
func TestMinIdleThreshold(t *testing.T) {
	policy := config.Default().Policy
	policy.MinIdleMin = 60
	sessions := []ras.SessionView{
		sess("Сидоров", "ib1", "Бухгалтерия", "1CV8C", 3, 210, 1),
		sess("Сидоров", "ib1", "Бухгалтерия", "1CV8C", 20, 700, 2),
		sess("Сидоров", "ib1", "Бухгалтерия", "1CV8C", 400, 1900, 3),
	}
	plan := Build(sessions, policy, now, nil)
	if got := findItem(t, plan, 2).Action; got != ActionKeep {
		t.Errorf("дубликат с простоем 20 мин должен остаться, got %q", got)
	}
	if got := findItem(t, plan, 3).Action; got != ActionTerminate {
		t.Errorf("дубликат с простоем 400 мин должен завершаться, got %q", got)
	}
}

// KeepPerGroup = 2 оставляет два самых актуальных сеанса.
func TestKeepPerGroup(t *testing.T) {
	policy := config.Default().Policy
	policy.KeepPerGroup = 2
	sessions := []ras.SessionView{
		sess("Смирнова", "ib1", "Бухгалтерия", "1CV8C", 1500, 2100, 1),
		sess("Смирнова", "ib1", "Бухгалтерия", "1CV8C", 25, 300, 2),
		sess("Смирнова", "ib1", "Бухгалтерия", "1CV8C", 640, 1250, 3),
	}
	plan := Build(sessions, policy, now, nil)
	if plan.Summary.ToTerminate != 1 {
		t.Fatalf("к завершению: got %d, want 1", plan.Summary.ToTerminate)
	}
	if got := findItem(t, plan, 1).Action; got != ActionTerminate {
		t.Errorf("самый старый сеанс должен завершаться, got %q", got)
	}
	if got := findItem(t, plan, 3).Action; got != ActionKeep {
		t.Errorf("второй по активности сеанс должен остаться, got %q", got)
	}
}

// Сеанс без единой отметки времени завершать нельзя.
func TestSessionWithoutTimestampsIsSkipped(t *testing.T) {
	s := sess("БезВремени", "ib1", "Бухгалтерия", "1CV8C", 0, 0, 1)
	s.Session.StartedAt = time.Time{}
	s.Session.LastActiveAt = time.Time{}
	plan := Build([]ras.SessionView{s, sess("БезВремени", "ib1", "Бухгалтерия", "1CV8C", 5, 60, 2)},
		config.Default().Policy, now, nil)
	if got := findItem(t, plan, 1).Action; got != ActionSkip {
		t.Errorf("сеанс без времени должен пропускаться, got %q", got)
	}
	if got := findItem(t, plan, 2).Action; got != ActionKeep {
		t.Errorf("единственный измеримый сеанс должен остаться, got %q", got)
	}
}

// Пустой снимок не должен приводить к ошибке.
func TestEmptySnapshot(t *testing.T) {
	plan := Build(nil, config.Default().Policy, now, nil)
	if plan.Summary.Total != 0 || plan.Summary.ToTerminate != 0 {
		t.Fatalf("unexpected summary: %+v", plan.Summary)
	}
	if len(plan.Targets()) != 0 {
		t.Fatal("targets должны быть пустыми")
	}
}
