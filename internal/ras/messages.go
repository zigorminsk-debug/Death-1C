package ras

import (
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/rascodec"
)

// Коды сообщений сервиса администрирования. Приведены только те,
// которые использует программа; полный набор совпадает с rac.exe.
const (
	msgAuthenticateRequest        = 9
	msgAddAuthenticationRequest   = 10
	msgGetClustersRequest         = 11
	msgGetClustersResponse        = 12
	msgGetInfobasesShortRequest   = 42
	msgGetInfobasesShortResponse  = 43
	msgGetSessionsRequest         = 65
	msgGetSessionsResponse        = 66
	msgGetSessionInfoRequest      = 69
	msgGetSessionInfoResponse     = 70
	msgTerminateSessionRequest    = 71
	msgInterruptServerCallRequest = 117
	msgGetAgentVersionRequest     = 135
	msgGetAgentVersionResponse    = 136
)

// Cluster — кластер серверов 1С.
type Cluster struct {
	UUID string
	Name string
	Host string
	Port int

	ExpirationTimeout          int
	LifetimeLimit              int
	MaxMemorySize              int
	MaxMemoryTimeLimit         int
	SecurityLevel              int
	SessionFaultToleranceLevel int
	LoadBalancingMode          int
	ErrorsCountThreshold       int
	KillProblemProcesses       bool
	KillByMemoryWithDump       bool
}

// Presentation — человекочитаемое имя кластера.
func (c Cluster) Presentation() string {
	if c.Name != "" {
		return c.Name
	}
	if c.Host != "" {
		return c.Host
	}
	return c.UUID
}

// Infobase — краткая карточка информационной базы.
type Infobase struct {
	UUID  string
	Name  string
	Descr string
}

// License — лицензия, выданная сеансу.
type License struct {
	FullName       string
	ShortPresent   string
	FullPresent    string
	Series         string
	IssuedByServer bool
	Net            bool
	LicenseType    int
	MaxUsersAll    int
	MaxUsersCur    int
	RmngrAddress   string
	RmngrPid       string
	RmngrPort      int
}

// Session — сеанс информационной базы.
type Session struct {
	UUID           string
	ID             int
	InfobaseID     string
	ConnectionID   string
	ProcessID      string
	AppID          string
	UserName       string
	Host           string
	Locale         string
	ClientIP       string
	DataSeparation string
	CurrentService string
	DbProcInfo     string
	DbProcTook     int

	StartedAt    time.Time
	LastActiveAt time.Time
	DbProcTookAt time.Time

	Hibernate              bool
	BlockedByDBMS          int
	BlockedByLS            int
	PassiveHibernateTime   int
	HibernateTerminateTime int

	CallsAll        int
	CallsLast5Min   int64
	BytesAll        int64
	BytesLast5Min   int64
	DBMSBytesAll    int64
	DBMSBytesLast5M int64

	DurationAll          int
	DurationAllDBMS      int
	DurationCurrent      int
	DurationCurrentDBMS  int
	DurationLast5Min     int64
	DurationLast5MinDBMS int64

	MemoryCurrent  int64
	MemoryLast5Min int64
	MemoryTotal    int64
	ReadCurrent    int64
	ReadTotal      int64
	WriteCurrent   int64
	WriteTotal     int64

	CPUTimeCurrent  int64
	CPUTimeLast5Min int64
	CPUTimeTotal    int64

	Licenses []License
}

// LastActivity возвращает момент последней активности сеанса,
// а если он не зафиксирован — момент запуска. Нулевое время означает
// «сервер не сообщил дату».
func (s Session) LastActivity() time.Time {
	if !s.LastActiveAt.IsZero() {
		return s.LastActiveAt
	}
	return s.StartedAt
}

// parseClusterInfo разбирает структуру ClusterInfo.
// Порядок полей соответствует эталонной сериализации 1С.
func parseClusterInfo(r *rascodec.Reader, version int32) Cluster {
	var c Cluster
	c.UUID = r.UUID()
	c.ExpirationTimeout = r.Int()
	c.Host = r.Str()
	c.LifetimeLimit = r.Int()
	c.Port = r.Short()
	c.MaxMemorySize = r.Int()
	c.MaxMemoryTimeLimit = r.Int()
	c.Name = r.Str()
	c.SecurityLevel = r.Int()
	c.SessionFaultToleranceLevel = r.Int()
	c.LoadBalancingMode = r.Int()
	c.ErrorsCountThreshold = r.Int()
	c.KillProblemProcesses = r.Bool()
	if version >= 9 {
		c.KillByMemoryWithDump = r.Bool()
	}
	return c
}

// parseInfobaseSummary разбирает структуру InfobaseSummaryInfo.
func parseInfobaseSummary(r *rascodec.Reader) Infobase {
	var ib Infobase
	ib.UUID = r.UUID()
	ib.Descr = r.Str()
	ib.Name = r.Str()
	return ib
}

// parseLicense разбирает структуру LicenseInfo.
// Порядок полей: FullName, FullPresentation, IssuedByServer, LicenseType,
// MaxUsersAll, MaxUsersCur, Net, RmngrAddress, RmngrPid, RmngrPort, Series,
// ShortPresentation.
func parseLicense(r *rascodec.Reader) License {
	var l License
	l.FullName = r.Str()
	l.FullPresent = r.Str()
	l.IssuedByServer = r.Bool()
	l.LicenseType = r.Int()
	l.MaxUsersAll = r.Int()
	l.MaxUsersCur = r.Int()
	l.Net = r.Bool()
	l.RmngrAddress = r.Str()
	l.RmngrPid = r.Str()
	l.RmngrPort = r.Int()
	l.Series = r.Str()
	l.ShortPresent = r.Str()
	return l
}

// parseSession разбирает структуру SessionInfo.
// Поля, добавленные в новых версиях протокола, читаются только при
// соответствующей согласованной версии эндпоинта.
func parseSession(r *rascodec.Reader, version int32) Session {
	var s Session
	s.UUID = r.UUID()
	s.AppID = r.Str()
	s.BlockedByDBMS = r.Int()
	s.BlockedByLS = r.Int()
	s.BytesAll = r.Long()
	s.BytesLast5Min = r.Long()
	s.CallsAll = r.Int()
	s.CallsLast5Min = r.Long()
	s.ConnectionID = r.UUID()
	s.DBMSBytesAll = r.Long()
	s.DBMSBytesLast5M = r.Long()
	s.DbProcInfo = r.Str()
	s.DbProcTook = r.Int()
	s.DbProcTookAt = r.Time()
	s.DurationAll = r.Int()
	s.DurationAllDBMS = r.Int()
	s.DurationCurrent = r.Int()
	s.DurationCurrentDBMS = r.Int()
	s.DurationLast5Min = r.Long()
	s.DurationLast5MinDBMS = r.Long()
	s.Host = r.Str()
	s.InfobaseID = r.UUID()
	s.LastActiveAt = r.Time()
	s.Hibernate = r.Bool()
	s.PassiveHibernateTime = r.Int()
	s.HibernateTerminateTime = r.Int()

	n := r.Size()
	if n > 0 && n < 4096 {
		s.Licenses = make([]License, 0, n)
		for i := 0; i < n && r.Err() == nil; i++ {
			s.Licenses = append(s.Licenses, parseLicense(r))
		}
	}

	s.Locale = r.Str()
	s.ProcessID = r.UUID()
	s.ID = r.Int()
	s.StartedAt = r.Time()
	s.UserName = r.Str()

	if version >= 4 {
		s.MemoryCurrent = r.Long()
		s.MemoryLast5Min = r.Long()
		s.MemoryTotal = r.Long()
		s.ReadCurrent = r.Long()
		_ = r.Long() // read last 5 min
		s.ReadTotal = r.Long()
		s.WriteCurrent = r.Long()
		_ = r.Long() // write last 5 min
		s.WriteTotal = r.Long()
	}
	if version >= 5 {
		_ = r.Int()  // duration current service
		_ = r.Long() // duration last 5 min service
		_ = r.Int()  // duration all service
		s.CurrentService = r.Str()
	}
	if version >= 6 {
		s.CPUTimeCurrent = r.Long()
		s.CPUTimeLast5Min = r.Long()
		s.CPUTimeTotal = r.Long()
	}
	if version >= 7 {
		s.DataSeparation = r.Str()
	}
	if version >= 10 {
		s.ClientIP = r.Str()
	}
	return s
}
