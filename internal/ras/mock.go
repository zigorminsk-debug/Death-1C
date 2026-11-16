package ras

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/rascodec"
)

// MockServer — учебный сервер администрирования 1С:Предприятие.
//
// Он говорит на том же протоколе, что и настоящий ras.exe, и нужен для двух задач:
//   - демонстрационный режим программы (можно посмотреть интерфейс и работу
//     очистки без доступа к боевому серверу 1С);
//   - сквозные тесты: кодек, рукопожатие, авторизация, списки и завершение сеансов
//     проверяются в GitHub Actions на каждом коммите.
type MockServer struct {
	ln   net.Listener
	addr string

	mu           sync.Mutex
	clusters     []Cluster
	infobases    []Infobase
	sessions     []SessionView
	agentVersion string
	user         string
	password     string
	maxVersion   string
	terminated   []string
	failAuth     bool
	closed       bool
	wg           sync.WaitGroup
}

// MockOptions — параметры учебного сервера.
type MockOptions struct {
	User         string // ожидаемый администратор кластера; пусто — авторизация не нужна
	Password     string
	AgentVersion string
	MaxVersion   string // максимальная поддерживаемая версия сервиса, по умолчанию 10.0
	FailAuth     bool   // всегда отвечать отказом на авторизацию (для тестов ошибок)
}

// NewMockServer поднимает учебный сервер на случайном свободном порту 127.0.0.1.
func NewMockServer(opts MockOptions) (*MockServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if opts.AgentVersion == "" {
		opts.AgentVersion = "8.3.24.1586"
	}
	if opts.MaxVersion == "" {
		opts.MaxVersion = "10.0"
	}
	s := &MockServer{
		ln:           ln,
		addr:         ln.Addr().String(),
		agentVersion: opts.AgentVersion,
		user:         opts.User,
		password:     opts.Password,
		maxVersion:   opts.MaxVersion,
		failAuth:     opts.FailAuth,
	}
	s.clusters, s.infobases, s.sessions = DemoData()
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Addr — адрес «хост:порт», на котором слушает сервер.
func (s *MockServer) Addr() string { return s.addr }

// Close останавливает сервер.
func (s *MockServer) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.ln.Close()
	s.wg.Wait()
}

// Terminated — идентификаторы сеансов, которые завершили через этот сервер.
func (s *MockServer) Terminated() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.terminated))
	copy(out, s.terminated)
	return out
}

// SessionsLeft — сколько сеансов осталось.
func (s *MockServer) SessionsLeft() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *MockServer) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer c.Close()
			s.serve(c)
		}()
	}
}

func (s *MockServer) serve(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(2 * time.Minute))

	// Negotiate: magic + версия протокола + версия кодека, без кадра пакета.
	neg := make([]byte, 12)
	if _, err := io.ReadFull(c, neg); err != nil {
		return
	}
	nr := rascodec.NewReader(bytes.NewReader(neg))
	magic := int32(nr.Int())
	if magic != protocolMagic {
		return
	}

	version := s.maxVersion
	major, _ := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	epVersion := int32(major)

	for {
		typ, data, err := readPacketFrom(c)
		if err != nil {
			return
		}
		switch typ {
		case pktConnect:
			if err := writePacketTo(c, pktConnectAck, nil); err != nil {
				return
			}
		case pktEndpointOpen:
			r := rascodec.NewReader(bytes.NewReader(data))
			_ = r.Str() // имя сервиса
			want := r.Str()
			if !s.versionSupported(want) {
				payload := s.failureAck(version, want)
				if err := writePacketTo(c, pktEndpointFailure, payload); err != nil {
					return
				}
				continue
			}
			version = want
			major, _ = strconv.Atoi(strings.SplitN(version, ".", 2)[0])
			epVersion = int32(major)
			buf := bytes.Buffer{}
			w := rascodec.NewWriter(&buf)
			w.Str(serviceName)
			w.Str(version)
			w.Nullable(1)
			w.Size(0)
			if err := writePacketTo(c, pktEndpointOpenAck, buf.Bytes()); err != nil {
				return
			}
		case pktEndpointClose:
			return
		case pktDisconnect:
			return
		case pktEndpointMessage:
			resp, respType, err := s.handleMessage(data, epVersion)
			if err != nil {
				return
			}
			if err := writePacketTo(c, pktEndpointMessage, resp); err != nil {
				return
			}
			_ = respType
		case pktKeepAlive:
			if err := writePacketTo(c, pktKeepAlive, nil); err != nil {
				return
			}
		default:
			return
		}
	}
}

func (s *MockServer) versionSupported(v string) bool {
	for _, sv := range serviceVersions {
		if sv == v {
			return versionLessOrEqual(v, s.maxVersion)
		}
	}
	return false
}

func versionLessOrEqual(a, b string) bool {
	pa := strings.SplitN(a, ".", 2)
	pb := strings.SplitN(b, ".", 2)
	na, _ := strconv.Atoi(pa[0])
	nb, _ := strconv.Atoi(pb[0])
	if na != nb {
		return na < nb
	}
	ma, _ := strconv.Atoi(secondOf(pa))
	mb, _ := strconv.Atoi(secondOf(pb))
	return ma <= mb
}

func secondOf(parts []string) string {
	if len(parts) == 2 {
		return parts[1]
	}
	return "0"
}

// failureAck собирает пакет отказа с подсказкой о поддерживаемых версиях,
// чтобы клиент смог понизить версию сервиса и повторить попытку.
func (s *MockServer) failureAck(maxSupported, requested string) []byte {
	supported := make([]string, 0, len(serviceVersions))
	for _, v := range serviceVersions {
		if versionLessOrEqual(v, maxSupported) {
			supported = append(supported, v)
		}
	}
	buf := bytes.Buffer{}
	w := rascodec.NewWriter(&buf)
	w.Str(serviceName)
	w.Str(requested)
	w.Nullable(0)
	w.Str("UnsupportedVersionException")
	w.Str(fmt.Sprintf("service %s version %s is not supported", serviceName, requested))
	w.Size(0)
	w.Str(serviceName)
	w.Str("supported=[" + strings.Join(supported, ", ") + "]")
	return buf.Bytes()
}

// handleMessage разбирает сообщение эндпоинта и готовит ответ.
func (s *MockServer) handleMessage(data []byte, epVersion int32) ([]byte, int, error) {
	r := rascodec.NewReader(bytes.NewReader(data))
	endpointID := r.Nullable()
	_ = r.Short()
	kind := byte(r.Byte())
	if kind != dataMessage {
		return nil, 0, fmt.Errorf("mock: неожиданный вид сообщения %d", kind)
	}
	msgType := r.Byte()
	payload := r.BytesRaw()
	if r.Err() != nil {
		return nil, 0, r.Err()
	}

	req := rascodec.NewReader(bytes.NewReader(payload))
	body := bytes.Buffer{}
	w := rascodec.NewWriter(&body)
	respType := 0

	switch msgType {
	case msgGetAgentVersionRequest:
		w.Str(s.agentVersion)
		respType = msgGetAgentVersionResponse
	case msgGetClustersRequest:
		s.mu.Lock()
		list := append([]Cluster(nil), s.clusters...)
		s.mu.Unlock()
		w.Size(len(list))
		for _, c := range list {
			formatCluster(w, c, epVersion)
		}
		respType = msgGetClustersResponse
	case msgAuthenticateRequest:
		_ = req.UUID()
		user := req.Str()
		pwd := req.Str()
		s.mu.Lock()
		fail := s.failAuth
		wantUser, wantPwd := s.user, s.password
		s.mu.Unlock()
		if fail || user != wantUser || pwd != wantPwd {
			return s.exceptionMessage(endpointID, "AccessDeniedException",
				"Cluster administrator authentication failed"), msgType, nil
		}
		return s.voidMessage(endpointID), 0, nil
	case msgAddAuthenticationRequest:
		return s.voidMessage(endpointID), 0, nil
	case msgGetInfobasesShortRequest:
		clusterID := req.UUID()
		s.mu.Lock()
		list := append([]Infobase(nil), s.infobases...)
		s.mu.Unlock()
		_ = clusterID
		w.Size(len(list))
		for _, ib := range list {
			formatInfobaseSummary(w, ib)
		}
		respType = msgGetInfobasesShortResponse
	case msgGetSessionsRequest:
		s.mu.Lock()
		list := append([]SessionView(nil), s.sessions...)
		s.mu.Unlock()
		w.Size(len(list))
		for _, sv := range list {
			formatSession(w, sv.Session, epVersion)
		}
		respType = msgGetSessionsResponse
	case msgTerminateSessionRequest:
		clusterID := req.UUID()
		sessionID := req.UUID()
		_ = req.Str()
		s.mu.Lock()
		found := false
		rest := s.sessions[:0]
		for _, sv := range s.sessions {
			if sv.UUID == sessionID && (clusterID == "" || sv.ClusterID == clusterID) {
				found = true
				s.terminated = append(s.terminated, sessionID)
				continue
			}
			rest = append(rest, sv)
		}
		s.sessions = rest
		s.mu.Unlock()
		if !found {
			return s.exceptionMessage(endpointID, "NotFoundException",
				"session "+sessionID+" not found"), msgType, nil
		}
		return s.voidMessage(endpointID), 0, nil
	case msgInterruptServerCallRequest:
		return s.voidMessage(endpointID), 0, nil
	default:
		return s.exceptionMessage(endpointID, "UnsupportedOperationException",
			fmt.Sprintf("mock server: сообщение %d не поддерживается", msgType)), msgType, nil
	}

	return s.messageEnvelope(endpointID, respType, body.Bytes()), respType, nil
}

func (s *MockServer) voidMessage(endpointID int) []byte {
	buf := bytes.Buffer{}
	w := rascodec.NewWriter(&buf)
	w.Nullable(endpointID)
	w.Short(int(rascodec.CodecVersion))
	w.Byte(int(dataVoid))
	return buf.Bytes()
}

func (s *MockServer) exceptionMessage(endpointID int, class, message string) []byte {
	buf := bytes.Buffer{}
	w := rascodec.NewWriter(&buf)
	w.Nullable(endpointID)
	w.Short(int(rascodec.CodecVersion))
	w.Byte(int(dataException))
	w.Str(class)
	w.Str(message)
	w.Str(class)
	w.Str(message)
	return buf.Bytes()
}

func (s *MockServer) messageEnvelope(endpointID, msgType int, body []byte) []byte {
	return endpointEnvelope(endpointID, msgType, body)
}

func writePacketTo(c net.Conn, typ byte, payload []byte) error {
	buf := bytes.Buffer{}
	w := rascodec.NewWriter(&buf)
	w.RawByte(typ)
	w.Size(len(payload))
	if len(payload) > 0 {
		w.Bytes(payload)
	}
	if err := w.Err(); err != nil {
		return err
	}
	_, err := c.Write(buf.Bytes())
	return err
}

func readPacketFrom(c net.Conn) (byte, []byte, error) {
	head := make([]byte, 1)
	if _, err := io.ReadFull(c, head); err != nil {
		return 0, nil, err
	}
	sr := rascodec.NewReader(c)
	size := sr.Size()
	if err := sr.Err(); err != nil {
		return 0, nil, err
	}
	if size < 0 || size > 64*1024*1024 {
		return 0, nil, fmt.Errorf("mock: недопустимый размер пакета %d", size)
	}
	data := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(c, data); err != nil {
			return 0, nil, err
		}
	}
	return head[0], data, nil
}

// ---------------------------------------------------------------------------
// Сериализация структур в учебный ответ (зеркало функций разбора).
// ---------------------------------------------------------------------------

func formatCluster(w *rascodec.Writer, c Cluster, version int32) {
	w.UUID(c.UUID)
	w.Int(c.ExpirationTimeout)
	w.Str(c.Host)
	w.Int(c.LifetimeLimit)
	w.Short(c.Port)
	w.Int(c.MaxMemorySize)
	w.Int(c.MaxMemoryTimeLimit)
	w.Str(c.Name)
	w.Int(c.SecurityLevel)
	w.Int(c.SessionFaultToleranceLevel)
	w.Int(c.LoadBalancingMode)
	w.Int(c.ErrorsCountThreshold)
	w.Bool(c.KillProblemProcesses)
	if version >= 9 {
		w.Bool(c.KillByMemoryWithDump)
	}
}

func formatInfobaseSummary(w *rascodec.Writer, ib Infobase) {
	w.UUID(ib.UUID)
	w.Str(ib.Descr)
	w.Str(ib.Name)
}

func formatLicense(w *rascodec.Writer, l License) {
	w.Str(l.FullName)
	w.Str(l.FullPresent)
	w.Bool(l.IssuedByServer)
	w.Int(l.LicenseType)
	w.Int(l.MaxUsersAll)
	w.Int(l.MaxUsersCur)
	w.Bool(l.Net)
	w.Str(l.RmngrAddress)
	w.Str(l.RmngrPid)
	w.Int(l.RmngrPort)
	w.Str(l.Series)
	w.Str(l.ShortPresent)
}

func formatSession(w *rascodec.Writer, s Session, version int32) {
	w.UUID(s.UUID)
	w.Str(s.AppID)
	w.Int(s.BlockedByDBMS)
	w.Int(s.BlockedByLS)
	w.Long(s.BytesAll)
	w.Long(s.BytesLast5Min)
	w.Int(s.CallsAll)
	w.Long(s.CallsLast5Min)
	w.UUID(s.ConnectionID)
	w.Long(s.DBMSBytesAll)
	w.Long(s.DBMSBytesLast5M)
	w.Str(s.DbProcInfo)
	w.Int(s.DbProcTook)
	w.Time(s.DbProcTookAt)
	w.Int(s.DurationAll)
	w.Int(s.DurationAllDBMS)
	w.Int(s.DurationCurrent)
	w.Int(s.DurationCurrentDBMS)
	w.Long(s.DurationLast5Min)
	w.Long(s.DurationLast5MinDBMS)
	w.Str(s.Host)
	w.UUID(s.InfobaseID)
	w.Time(s.LastActiveAt)
	w.Bool(s.Hibernate)
	w.Int(s.PassiveHibernateTime)
	w.Int(s.HibernateTerminateTime)
	w.Size(len(s.Licenses))
	for _, l := range s.Licenses {
		formatLicense(w, l)
	}
	w.Str(s.Locale)
	w.UUID(s.ProcessID)
	w.Int(s.ID)
	w.Time(s.StartedAt)
	w.Str(s.UserName)
	if version >= 4 {
		w.Long(s.MemoryCurrent)
		w.Long(s.MemoryLast5Min)
		w.Long(s.MemoryTotal)
		w.Long(s.ReadCurrent)
		w.Long(0)
		w.Long(s.ReadTotal)
		w.Long(s.WriteCurrent)
		w.Long(0)
		w.Long(s.WriteTotal)
	}
	if version >= 5 {
		w.Int(0)
		w.Long(0)
		w.Int(0)
		w.Str(s.CurrentService)
	}
	if version >= 6 {
		w.Long(s.CPUTimeCurrent)
		w.Long(s.CPUTimeLast5Min)
		w.Long(s.CPUTimeTotal)
	}
	if version >= 7 {
		w.Str(s.DataSeparation)
	}
	if version >= 10 {
		w.Str(s.ClientIP)
	}
}

// ---------------------------------------------------------------------------
// Демонстрационные данные
// ---------------------------------------------------------------------------

// DemoData возвращает учебный набор: один кластер, три информационные базы
// и сеансы с «задвоенными» подключениями пользователей — ровно та картина,
// ради которой написана программа.
func DemoData() ([]Cluster, []Infobase, []SessionView) {
	clusterID := "2dbd2cce-fddc-11e8-a982-2c4d544cf050"
	clusters := []Cluster{{
		UUID:                       clusterID,
		Name:                       "Демонстрационный кластер",
		Host:                       "srv-1c-01",
		Port:                       1541,
		ExpirationTimeout:          120,
		LifetimeLimit:              86400,
		MaxMemorySize:              8388608,
		MaxMemoryTimeLimit:         300,
		SecurityLevel:              0,
		SessionFaultToleranceLevel: 2,
		LoadBalancingMode:          0,
		ErrorsCountThreshold:       10,
		KillProblemProcesses:       true,
	}}

	ibAccounting := "aaaaaaaa-1111-2222-3333-444444444444"
	ibPayroll := "bbbbbbbb-1111-2222-3333-444444444444"
	ibArchive := "cccccccc-1111-2222-3333-444444444444"
	infobases := []Infobase{
		{UUID: ibAccounting, Name: "Бухгалтерия", Descr: "Основная рабочая база"},
		{UUID: ibPayroll, Name: "Зарплата", Descr: "Расчёт заработной платы"},
		{UUID: ibArchive, Name: "Архив2023", Descr: "Архивная копия"},
	}

	now := time.Now()
	type spec struct {
		user     string
		ib       string
		ibName   string
		app      string
		host     string
		idleMin  int
		ageMin   int
		hiber    bool
		clientIP string
	}
	specs := []spec{
		{"Иванова Мария", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-04", 4, 620, false, "10.10.4.21"},
		{"Иванова Мария", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-04", 185, 1480, true, "10.10.4.21"},
		{"Иванова Мария", ibAccounting, "Бухгалтерия", "1CV8", "WS-BUH-04", 1450, 2900, true, "10.10.4.21"},
		{"Петров Алексей", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-07", 12, 430, false, "10.10.4.35"},
		{"Петров Алексей", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-07", 260, 1900, true, "10.10.4.35"},
		{"Сидоров Олег", ibPayroll, "Зарплата", "1CV8C", "WS-HR-02", 3, 210, false, "10.10.7.12"},
		{"Сидоров Олег", ibPayroll, "Зарплата", "1CV8C", "WS-HR-02", 95, 700, false, "10.10.7.12"},
		{"Козлова Анна", ibPayroll, "Зарплата", "1CV8C", "WS-HR-05", 47, 380, false, "10.10.7.18"},
		{"Козлова Анна", ibArchive, "Архив2023", "1CV8C", "WS-HR-05", 320, 1600, true, "10.10.7.18"},
		{"", ibAccounting, "Бухгалтерия", "BackgroundJob", "srv-1c-01", 1, 86400, false, ""},
		{"", ibAccounting, "Бухгалтерия", "BackgroundJob", "srv-1c-01", 2, 86400, false, ""},
		{"Администратор", ibAccounting, "Бухгалтерия", "SrvrConsole", "WS-ADMIN-01", 30, 120, false, "10.10.1.5"},
		{"НочнойОбмен", ibArchive, "Архив2023", "1CV8", "srv-exchange", 900, 4000, true, "10.10.9.9"},
		{"Смирнова Ольга", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-11", 25, 300, false, "10.10.4.44"},
		{"Смирнова Ольга", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-11", 640, 1250, true, "10.10.4.44"},
		{"Смирнова Ольга", ibAccounting, "Бухгалтерия", "1CV8C", "WS-BUH-11", 1500, 2100, true, "10.10.4.44"},
	}

	sessions := make([]SessionView, 0, len(specs))
	for i, sp := range specs {
		started := now.Add(-time.Duration(sp.ageMin) * time.Minute)
		lastActive := now.Add(-time.Duration(sp.idleMin) * time.Minute)
		sess := Session{
			UUID:               fmt.Sprintf("d000000%d-0000-0000-0000-%012d", i, i+1),
			ID:                 1000 + i,
			InfobaseID:         sp.ib,
			ConnectionID:       fmt.Sprintf("c000000%d-0000-0000-0000-%012d", i, i+1),
			ProcessID:          "e1e1e1e1-2222-3333-4444-555555555555",
			AppID:              sp.app,
			UserName:           sp.user,
			Host:               sp.host,
			Locale:             "ru_RU",
			ClientIP:           sp.clientIP,
			StartedAt:          started,
			LastActiveAt:       lastActive,
			Hibernate:          sp.hiber,
			CallsAll:           120 + i*37,
			CallsLast5Min:      int64(i % 5),
			BytesAll:           int64(1200000 + i*88000),
			MemoryCurrent:      int64(18_000_000 + i*1_500_000),
			MemoryTotal:        int64(95_000_000 + i*3_000_000),
			DurationCurrent:    15 + i,
			DurationAll:        3600 + i*120,
			PassiveHibernateTime: 1200,
			HibernateTerminateTime: 86400,
		}
		if sp.user != "" {
			sess.Licenses = []License{{
				FullName:       sp.user,
				ShortPresent:   "1С:Предприятие 8.3 (x86-64) клиент",
				FullPresent:    "1С:Предприятие 8.3 (x86-64) клиентская лицензия на 1 рабочее место",
				Series:         "ENWG",
				IssuedByServer: true,
				Net:            true,
				LicenseType:    1,
				MaxUsersAll:    100,
				MaxUsersCur:    42,
				RmngrAddress:   "srv-1c-01",
				RmngrPort:      1541,
			}}
		}
		sessions = append(sessions, SessionView{
			Session:      sess,
			ClusterID:    clusterID,
			ClusterName:  clusters[0].Name,
			InfobaseName: sp.ibName,
		})
	}
	return clusters, infobases, sessions
}

// DemoClient подключает учебный сервер и возвращает готовый к работе клиент.
// Функция используется демонстрационным режимом и тестами.
func DemoClient(ctx context.Context, srv *MockServer, timeout time.Duration) (*Client, error) {
	return Dial(ctx, srv.Addr(), timeout)
}
