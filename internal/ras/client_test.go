package ras

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/rascodec"
)

const testTimeout = 10 * time.Second

// Сквозная проверка протокола: рукопожатие, авторизация, списки и завершение сеанса
// на учебном сервере. Тест выполняется в CI на каждом коммите.
func TestMockRoundTrip(t *testing.T) {
	srv, err := NewMockServer(MockOptions{})
	if err != nil {
		t.Fatalf("не удалось поднять учебный сервер: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, err := Dial(ctx, srv.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer cl.Close()

	if v := cl.EndpointVersion(); v < 3 || v > 10 {
		t.Errorf("версия эндпоинта = %d, ожидалась 3…10", v)
	}

	ver, err := cl.AgentVersion(ctx)
	if err != nil {
		t.Fatalf("версия агента: %v", err)
	}
	if !strings.HasPrefix(ver, "8.3") {
		t.Errorf("версия агента = %q, ожидалось 8.3.x", ver)
	}

	clusters, err := cl.Clusters(ctx)
	if err != nil {
		t.Fatalf("список кластеров: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("кластеров = %d, ожидался 1", len(clusters))
	}
	if clusters[0].Name == "" || clusters[0].Host != "srv-1c-01" {
		t.Errorf("кластер прочитан некорректно: %+v", clusters[0])
	}

	if err := cl.Authenticate(ctx, clusters[0].UUID, "", ""); err != nil {
		t.Fatalf("авторизация: %v", err)
	}

	ibs, err := cl.Infobases(ctx, clusters[0].UUID)
	if err != nil {
		t.Fatalf("список баз: %v", err)
	}
	if len(ibs) != 3 {
		t.Fatalf("баз = %d, ожидалось 3", len(ibs))
	}

	sessions, err := cl.Sessions(ctx, clusters[0].UUID)
	if err != nil {
		t.Fatalf("список сеансов: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("учебный сервер вернул пустой список сеансов")
	}
	first := sessions[0]
	if first.UUID == "" || first.AppID == "" || first.Host == "" {
		t.Errorf("сеанс прочитан некорректно: %+v", first)
	}
	if first.LastActiveAt.IsZero() || first.StartedAt.IsZero() {
		t.Errorf("у сеанса нет отметок времени: started=%v last=%v", first.StartedAt, first.LastActiveAt)
	}
	if first.LastActiveAt.Before(first.StartedAt) {
		t.Errorf("последняя активность раньше запуска: %v < %v", first.LastActiveAt, first.StartedAt)
	}

	before := srv.SessionsLeft()
	if err := cl.TerminateSession(ctx, clusters[0].UUID, first.UUID, "тестовое завершение"); err != nil {
		t.Fatalf("завершение сеанса: %v", err)
	}
	if after := srv.SessionsLeft(); after != before-1 {
		t.Errorf("сеансов на сервере: было %d, стало %d", before, after)
	}
	if len(srv.Terminated()) != 1 || srv.Terminated()[0] != first.UUID {
		t.Errorf("сервер зафиксировал не то завершение: %v", srv.Terminated())
	}

	// Повторное завершение того же сеанса должно вернуть понятную ошибку.
	err = cl.TerminateSession(ctx, clusters[0].UUID, first.UUID, "ещё раз")
	if err == nil {
		t.Error("ожидалась ошибка при завершении уже отсутствующего сеанса")
	}
}

// Полный опрос сервера одной функцией — так его использует программа.
func TestCollectSnapshot(t *testing.T) {
	srv, err := NewMockServer(MockOptions{User: "clusteradmin", Password: "secret"})
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, err := Dial(ctx, srv.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer cl.Close()

	if _, err := CollectSnapshot(ctx, cl, "clusteradmin", "wrong"); err == nil {
		t.Error("с неверным паролем опрос должен завершаться ошибкой")
	}

	snap, err := CollectSnapshot(ctx, cl, "clusteradmin", "secret")
	if err != nil {
		t.Fatalf("опрос: %v", err)
	}
	if len(snap.Sessions) == 0 {
		t.Fatal("в снимке нет сеансов")
	}
	if snap.AgentVersion == "" {
		t.Error("в снимке нет версии агента")
	}
	foundIBName := false
	for _, sv := range snap.Sessions {
		if sv.InfobaseName != "" && sv.InfobaseName != sv.InfobaseID {
			foundIBName = true
		}
		if sv.ClusterID == "" {
			t.Error("у сеанса не заполнен идентификатор кластера")
		}
	}
	if !foundIBName {
		t.Error("имена информационных баз не подставились в сеансы")
	}
}

// Клиент должен сам понизить версию сервиса, если сервер её не поддерживает.
func TestVersionNegotiationFallback(t *testing.T) {
	srv, err := NewMockServer(MockOptions{MaxVersion: "7.0"})
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, err := Dial(ctx, srv.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("подключение с понижением версии: %v", err)
	}
	defer cl.Close()

	if cl.EndpointVersion() != 7 {
		t.Errorf("версия эндпоинта = %d, ожидалась 7", cl.EndpointVersion())
	}
	sessions, err := cl.Sessions(ctx, "2dbd2cce-fddc-11e8-a982-2c4d544cf050")
	if err != nil {
		t.Fatalf("список сеансов на версии 7: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("пустой список сеансов на версии 7")
	}
	if sessions[0].ClientIP != "" {
		t.Errorf("поле client-ip появилось на версии 7: %q", sessions[0].ClientIP)
	}
}

// Отказ сервера должен приходить как читаемая ошибка, а не как обрыв соединения.
func TestFailureErrorIsReadable(t *testing.T) {
	srv, err := NewMockServer(MockOptions{FailAuth: true})
	if err != nil {
		t.Fatalf("сервер: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cl, err := Dial(ctx, srv.Addr(), 5*time.Second)
	if err != nil {
		t.Fatalf("подключение: %v", err)
	}
	defer cl.Close()

	err = cl.Authenticate(ctx, "cluster", "u", "p")
	if err == nil {
		t.Fatal("ожидался отказ авторизации")
	}
	if !strings.Contains(err.Error(), "авториз") && !strings.Contains(err.Error(), "Access") {
		t.Errorf("текст ошибки не понятен администратору: %v", err)
	}
}

// Проверка кадра запроса сеансов: байт-в-байт как у эталонного rac.exe.
func TestEndpointEnvelopeGolden(t *testing.T) {
	uuid := "2dbd2cce-fddc-11e8-a982-2c4d544cf050"
	b, err := rascodec.ParseUUID(uuid)
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}

	got := endpointEnvelope(1, msgGetSessionsRequest, b[:])
	want := []byte{
		0x01,       // endpointId = 1 (nullable)
		0x01, 0x00, // формат кодирования = 256
		0x01,                        // вид нагрузки = MESSAGE
		byte(msgGetSessionsRequest), // код сообщения = 65
	}
	want = append(want, b[:]...)
	if len(got) != len(want) {
		t.Fatalf("длина кадра = %d, ожидалась %d (%x)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("байт %d: got %02x, want %02x (весь кадр %x)", i, got[i], want[i], got)
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := map[string]string{
		"":               "localhost:1545",
		"srv-1c":         "srv-1c:1545",
		"srv-1c:2545":    "srv-1c:2545",
		"  10.0.0.5  ":   "10.0.0.5:1545",
		"10.0.0.5:1545 ": "10.0.0.5:1545",
	}
	for in, want := range cases {
		if got := NormalizeAddr(in); got != want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
