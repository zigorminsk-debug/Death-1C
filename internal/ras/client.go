package ras

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/rascodec"
)

// Client — клиент сервера администрирования 1С:Предприятие (RAS, порт 1545).
//
// Работает по нативному протоколу rac.exe, поэтому программе не нужна
// установленная платформа 1С: достаточно сетевой доступности порта RAS.
type Client struct {
	c      *conn
	authed map[string]bool
}

// Dial подключается к серверу администрирования и выполняет рукопожатие.
// Адрес задаётся в виде «хост:порт», порт по умолчанию — 1545.
func Dial(ctx context.Context, addr string, timeout time.Duration) (*Client, error) {
	c, err := dial(ctx, NormalizeAddr(addr), timeout)
	if err != nil {
		return nil, err
	}
	return &Client{c: c, authed: map[string]bool{}}, nil
}

// NormalizeAddr добавляет порт 1545, если он не указан.
func NormalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "localhost:1545"
	}
	// IPv6 в квадратных скобках не трогаем.
	if strings.Count(addr, ":") == 1 {
		return addr
	}
	if strings.Contains(addr, "]") {
		return addr
	}
	return addr + ":1545"
}

// Addr — адрес соединения.
func (cl *Client) Addr() string { return cl.c.addr }

// EndpointVersion — согласованная версия сервиса администрирования (3…10).
func (cl *Client) EndpointVersion() int32 { return cl.c.version }

// Trace возвращает протокол рукопожатия и отказов текущего соединения.
func (cl *Client) Trace() []string {
	cl.c.mu.Lock()
	defer cl.c.mu.Unlock()
	out := make([]string, len(cl.c.trace))
	copy(out, cl.c.trace)
	return out
}

// Close закрывает соединение.
func (cl *Client) Close() error { return cl.c.close() }

// AgentVersion возвращает версию агента сервера 1С (например «8.3.24.1586»).
func (cl *Client) AgentVersion(ctx context.Context) (string, error) {
	data, err := cl.c.invoke(ctx, msgGetAgentVersionRequest, nil)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	r := rascodec.NewReader(bytes.NewReader(data))
	return r.Str(), r.Err()
}

// Clusters возвращает список кластеров, зарегистрированных на агенте.
//
// Если сервер отвечает на первый запрос исключением о «соединении с кластером
// серверов» (ras принял версию сервиса новее, чем та, на которой говорит агент
// кластера), эндпоинт открывается заново на версии ступенью ниже и запрос
// повторяется — так программа работает и с платформами, где ras и ragent
// договориться между собой не могут.
func (cl *Client) Clusters(ctx context.Context) ([]Cluster, error) {
	for attempt := 0; attempt < 6; attempt++ {
		data, err := cl.c.invoke(ctx, msgGetClustersRequest, nil)
		if err == nil {
			r := rascodec.NewReader(bytes.NewReader(data))
			n := r.Size()
			out := make([]Cluster, 0, n)
			for i := 0; i < n && r.Err() == nil; i++ {
				out = append(out, parseClusterInfo(r, cl.c.version))
			}
			if e := r.Err(); e != nil {
				return nil, fmt.Errorf("разбор списка кластеров: %w", e)
			}
			return out, nil
		}
		if !isVersionThrow(err) || !cl.c.downgrade(ctx) {
			return nil, err
		}
	}
}

// Authenticate авторизует администратора кластера.
// Пустые логин и пароль допустимы, если у кластера нет администраторов.
func (cl *Client) Authenticate(ctx context.Context, clusterID, user, password string) error {
	_, err := cl.c.invoke(ctx, msgAuthenticateRequest, func(w *rascodec.Writer) {
		w.UUID(clusterID)
		w.Str(user)
		w.Str(password)
	})
	if err != nil {
		return authError(err)
	}
	cl.c.mu.Lock()
	cl.authed[clusterID] = true
	cl.c.mu.Unlock()
	return nil
}

// AuthenticateInfobase выполняет авторизацию в информационных базах кластера
// (нужна для баз, у которых задан собственный администратор).
func (cl *Client) AuthenticateInfobase(ctx context.Context, clusterID, user, password string) error {
	_, err := cl.c.invoke(ctx, msgAddAuthenticationRequest, func(w *rascodec.Writer) {
		w.UUID(clusterID)
		w.Str(user)
		w.Str(password)
	})
	return err
}

// Infobases возвращает краткий список информационных баз кластера.
func (cl *Client) Infobases(ctx context.Context, clusterID string) ([]Infobase, error) {
	data, err := cl.c.invoke(ctx, msgGetInfobasesShortRequest, func(w *rascodec.Writer) {
		w.UUID(clusterID)
	})
	if err != nil {
		return nil, err
	}
	r := rascodec.NewReader(bytes.NewReader(data))
	n := r.Size()
	out := make([]Infobase, 0, n)
	for i := 0; i < n && r.Err() == nil; i++ {
		out = append(out, parseInfobaseSummary(r))
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("разбор списка информационных баз: %w", err)
	}
	return out, nil
}

// Sessions возвращает все сеансы кластера.
func (cl *Client) Sessions(ctx context.Context, clusterID string) ([]Session, error) {
	data, err := cl.c.invoke(ctx, msgGetSessionsRequest, func(w *rascodec.Writer) {
		w.UUID(clusterID)
	})
	if err != nil {
		return nil, err
	}
	r := rascodec.NewReader(bytes.NewReader(data))
	n := r.Size()
	if n > 200000 {
		return nil, fmt.Errorf("сервер сообщил неправдоподобное число сеансов: %d", n)
	}
	out := make([]Session, 0, n)
	for i := 0; i < n && r.Err() == nil; i++ {
		out = append(out, parseSession(r, cl.c.version))
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("разбор списка сеансов: %w", err)
	}
	return out, nil
}

// TerminateSession принудительно завершает сеанс.
func (cl *Client) TerminateSession(ctx context.Context, clusterID, sessionID, message string) error {
	_, err := cl.c.invoke(ctx, msgTerminateSessionRequest, func(w *rascodec.Writer) {
		w.UUID(clusterID)
		w.UUID(sessionID)
		w.Str(message)
	})
	return err
}

// InterruptCurrentCall прерывает текущий серверный вызов сеанса.
// Используется как мягкая альтернатива принудительному завершению.
func (cl *Client) InterruptCurrentCall(ctx context.Context, clusterID, sessionID, message string) error {
	_, err := cl.c.invoke(ctx, msgInterruptServerCallRequest, func(w *rascodec.Writer) {
		w.UUID(clusterID)
		w.UUID(sessionID)
		w.Str(message)
	})
	return err
}

func authError(err error) error {
	var fe *FailureError
	if errors.As(err, &fe) {
		text := strings.ToLower(fe.Error())
		if strings.Contains(text, "auth") || strings.Contains(text, "password") ||
			strings.Contains(text, "парол") || strings.Contains(text, "администратор") {
			return fmt.Errorf("авторизация в кластере не выполнена: %w", err)
		}
	}
	return err
}

// SessionView — сеанс с привязкой к кластеру и информационной базе.
type SessionView struct {
	Session
	ClusterID    string
	ClusterName  string
	InfobaseName string
}

// Snapshot — полное состояние сервера администрирования за один опрос.
type Snapshot struct {
	Addr            string
	AgentVersion    string
	EndpointVersion int32
	FetchedAt       time.Time
	Took            time.Duration
	Clusters        []Cluster
	Infobases       []Infobase
	Sessions        []SessionView
	// Errors — некритичные ошибки отдельных списков: остальные данные достоверны.
	Errors []string
}

// CollectSnapshot опрашивает сервер: кластеры → авторизация → базы → сеансы.
// Ошибка в одном кластере не прерывает опрос остальных.
func CollectSnapshot(ctx context.Context, cl *Client, user, password string) (*Snapshot, error) {
	start := time.Now()
	snap := &Snapshot{
		Addr:            cl.Addr(),
		EndpointVersion: cl.EndpointVersion(),
		FetchedAt:       start,
	}

	if v, err := cl.AgentVersion(ctx); err == nil {
		snap.AgentVersion = v
	} else {
		snap.Errors = append(snap.Errors, "версия агента: "+err.Error())
	}

	clusters, err := cl.Clusters(ctx)
	if err != nil {
		return nil, err
	}
	snap.Clusters = clusters
	if len(clusters) == 0 {
		return nil, errors.New("на агенте сервера 1С не зарегистрировано ни одного кластера")
	}

	ibNames := map[string]string{}
	for _, c := range clusters {
		if err := cl.Authenticate(ctx, c.UUID, user, password); err != nil {
			return nil, fmt.Errorf("кластер %s: %w", c.Presentation(), err)
		}

		if ibs, err := cl.Infobases(ctx, c.UUID); err != nil {
			snap.Errors = append(snap.Errors, fmt.Sprintf("кластер %s: список баз: %v", c.Presentation(), err))
		} else {
			snap.Infobases = append(snap.Infobases, ibs...)
			for _, ib := range ibs {
				ibNames[ib.UUID] = ib.Name
			}
		}

		sessions, err := cl.Sessions(ctx, c.UUID)
		if err != nil {
			snap.Errors = append(snap.Errors, fmt.Sprintf("кластер %s: список сеансов: %v", c.Presentation(), err))
			continue
		}
		for _, s := range sessions {
			name := ibNames[s.InfobaseID]
			if name == "" {
				name = s.InfobaseID
			}
			snap.Sessions = append(snap.Sessions, SessionView{
				Session:      s,
				ClusterID:    c.UUID,
				ClusterName:  c.Presentation(),
				InfobaseName: name,
			})
		}
	}
	snap.Took = time.Since(start)
	return snap, nil
}
