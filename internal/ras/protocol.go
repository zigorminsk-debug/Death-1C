package ras

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/rascodec"
)

// Типы пакетов транспортного уровня RAS.
const (
	pktNegotiate       byte = 0
	pktConnect         byte = 1
	pktConnectAck      byte = 2
	pktDisconnect      byte = 4
	pktEndpointOpen    byte = 11
	pktEndpointOpenAck byte = 12
	pktEndpointClose   byte = 13
	pktEndpointMessage byte = 14
	pktEndpointFailure byte = 15
	pktKeepAlive       byte = 16
)

// Виды полезной нагрузки сообщения эндпоинта.
const (
	dataVoid      byte = 0
	dataMessage   byte = 1
	dataException byte = 255
)

// Параметры рукопожатия. Значения совпадают с эталонным клиентом rac.exe.
const (
	protocolMagic   int32 = 475223888
	protocolVersion int32 = 256
	serviceName           = "v8.service.Admin.Cluster"
)

// serviceVersions — поддерживаемые версии сервиса администрирования,
// от старых к новым. Согласование идёт сверху вниз.
var serviceVersions = []string{"1.0", "2.0", "3.0", "4.0", "5.0", "6.0", "7.0", "8.0", "9.0", "10.0"}

var supportedRe = regexp.MustCompile(`(?m)supported=(.*?)]`)

// parseOpenAck разбирает ответ на открытие эндпоинта: сервис, версия, ид.
func parseOpenAck(data []byte) (string, int, error) {
	r := rascodec.NewReader(bytes.NewReader(data))
	_ = r.Str()    // имя сервиса
	ver := r.Str() // согласованная версия
	id := r.Nullable()
	if e := r.Err(); e != nil {
		return "", 0, e
	}
	return ver, id, nil
}

// reopen открывает на том же соединении новый эндпоинт сервиса администрирования
// с указанной версией. Нужно серверам, у которых ras принимает новую версию,
// но не может проксировать запросы к агенту кластера (ragent) на ней: такой
// сервер на первый же запрос отвечает исключением swp.UnknownThrowable
// («Ошибка установки соединения с кластером серверов»).
func (c *conn) reopen(ctx context.Context, ver string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcp == nil {
		return errors.New("соединение с сервером администрирования 1С закрыто")
	}
	data, err := c.exchange(ctx, pktEndpointOpen, func(w *rascodec.Writer) {
		w.Str(serviceName)
		w.Str(ver)
		w.Size(0)
	}, pktEndpointOpenAck)
	if err != nil {
		return err
	}
	got, id, perr := parseOpenAck(data)
	if perr != nil {
		return perr
	}
	major, convErr := strconv.Atoi(strings.SplitN(got, ".", 2)[0])
	if convErr != nil || major <= 0 {
		major = 10
	}
	c.endpoint = int32(id)
	c.version = int32(major)
	c.verStr = got
	c.opened = true
	c.tracef("endpoint открыт заново: версия %s, id=%d", got, id)
	return nil
}

// downgrade открывает эндпоинт на версии службы ступенью ниже.
// Возвращает false, если пробовать больше нечего.
func (c *conn) downgrade(ctx context.Context) bool {
	next := prevServiceVersion(c.verStr)
	if next == "" {
		return false
	}
	c.tracef("понизаю версию сервиса администрирования: %s → %s", c.verStr, next)
	if err := c.reopen(ctx, next); err != nil {
		c.tracef("endpoint open %s: %v", next, err)
		return false
	}
	return true
}

// isVersionThrow распознаёт исключение сервера, которое возникает из-за
// несовпадения версии сервиса администрирования между ras и агентом кластера:
// в тексте такого исключения про версии ничего не сказано, поэтому обычный
// разбор «supported=...» его не поймает.
func isVersionThrow(err error) bool {
	var fe *FailureError
	if !errors.As(err, &fe) {
		return false
	}
	t := strings.ToLower(fe.Error())
	return strings.Contains(t, "unknownthrowable") || strings.Contains(t, "соединения с кластером")
}

// prevServiceVersion возвращает версию сервиса на ступень старее указанной.
func prevServiceVersion(v string) string {
	for i := len(serviceVersions) - 1; i > 0; i-- {
		if serviceVersions[i] == v {
			return serviceVersions[i-1]
		}
	}
	return ""
}

// FailureError — отказ сервера администрирования с расшифровкой причины.
type FailureError struct {
	ServiceID  string
	Version    string
	ClassCause string
	Message    string
	CauseSvc   string
	CauseMsg   string
	Trace      []string
}

func (e *FailureError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.CauseMsg
	} else if e.CauseMsg != "" && e.CauseMsg != msg {
		msg = strings.TrimSpace(msg + " — " + e.CauseMsg)
	}
	if msg == "" {
		msg = "сервер администрирования 1С вернул отказ без пояснения"
	}
	if e.ClassCause != "" && !strings.Contains(msg, e.ClassCause) {
		msg = e.ClassCause + ": " + msg
	}
	return msg
}

// SupportedVersion извлекает из текста отказа список поддерживаемых версий
// и возвращает самую старшую подходящую. Пустая строка — версия не определена.
func (e *FailureError) SupportedVersion() string {
	m := supportedRe.FindStringSubmatch(e.CauseMsg + " " + e.Message)
	if len(m) < 2 {
		return ""
	}
	for i := len(serviceVersions) - 1; i >= 0; i-- {
		if strings.Contains(m[1], serviceVersions[i]) {
			return serviceVersions[i]
		}
	}
	return ""
}

// conn — одно TCP-соединение с сервером администрирования (RAS, порт 1545).
// Протокол строго запрос/ответный, поэтому все вызовы сериализованы мьютексом.
type conn struct {
	addr    string
	timeout time.Duration

	mu       sync.Mutex
	tcp      net.Conn
	opened   bool
	endpoint int32
	version  int32

	// trace — протокол рукопожатия и отказов: помогает понять по журналу
	// и по окну проверки, на каком именно шаге сервер ответил отказом.
	trace []string

	// verStr — строка версии сервиса, на которой открыт текущий эндпоинт.
	verStr string
}

// tracef добавляет шаг трассировки соединения.
func (c *conn) tracef(format string, args ...interface{}) {
	c.trace = append(c.trace, fmt.Sprintf(format, args...))
}

// dial устанавливает соединение и выполняет полное рукопожатие:
// negotiate → connect → endpoint open (с подбором версии сервиса).
func dial(ctx context.Context, addr string, timeout time.Duration) (*conn, error) {
	d := net.Dialer{Timeout: timeout}
	tcp, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к серверу администрирования %s: %w", addr, err)
	}
	c := &conn{addr: addr, timeout: timeout, tcp: tcp}
	if err := c.handshake(ctx); err != nil {
		_ = tcp.Close()
		return nil, err
	}
	return c, nil
}

func (c *conn) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *conn) closeLocked() error {
	c.opened = false
	c.endpoint = 0
	c.version = 0
	if c.tcp == nil {
		return nil
	}
	err := c.tcp.Close()
	c.tcp = nil
	return err
}

func (c *conn) setDeadline(ctx context.Context) {
	dl := time.Now().Add(c.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	if c.tcp != nil {
		_ = c.tcp.SetDeadline(dl)
	}
}

// handshake выполняет согласование протокола и открытие сервисного эндпоинта.
func (c *conn) handshake(ctx context.Context) error {
	c.setDeadline(ctx)

	// 1. Negotiate — единственное сообщение, которое уходит без кадра пакета.
	neg := bytes.Buffer{}
	nw := rascodec.NewWriter(&neg)
	nw.Int(int(protocolMagic))
	nw.Short(int(protocolVersion))
	nw.Short(int(rascodec.CodecVersion))
	if err := nw.Err(); err != nil {
		return err
	}
	if _, err := c.tcp.Write(neg.Bytes()); err != nil {
		return fmt.Errorf("negotiate: %w", err)
	}
	c.tracef("negotiate: magic=%d, протокол=256, кодек=256 отправлены", protocolMagic)

	// 2. Connect — пустой набор параметров.
	if _, err := c.exchange(ctx, pktConnect, func(w *rascodec.Writer) { w.Size(0) }, pktConnectAck); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	c.tracef("connect: сервер подтвердил соединение")

	// 3. Endpoint open — сначала пробуем самую новую версию сервиса.
	version := serviceVersions[len(serviceVersions)-1]
	for attempt := 0; attempt < len(serviceVersions); attempt++ {
		try := version
		data, err := c.exchange(ctx, pktEndpointOpen, func(w *rascodec.Writer) {
			w.Str(serviceName)
			w.Str(try)
			w.Size(0)
		}, pktEndpointOpenAck)
		if err == nil {
			ver, id, perr := parseOpenAck(data)
			if perr != nil {
				return fmt.Errorf("endpoint open: разбор ответа: %w", perr)
			}
			major, convErr := strconv.Atoi(strings.SplitN(ver, ".", 2)[0])
			if convErr != nil || major <= 0 {
				major = 10
			}
			c.endpoint = int32(id)
			c.version = int32(major)
			c.verStr = ver
			c.opened = true
			c.tracef("endpoint open: сервис %s, версия %s, id=%d", serviceName, ver, id)
			return nil
		}
		var fe *FailureError
		if !errors.As(err, &fe) {
			return fmt.Errorf("endpoint open (%s): %w", version, err)
		}
		// Часть версий сервера вместо аккуратного отказа с перечнем версий
		// бросает исключение (swp.UnknownThrowable и подобные). Поэтому при
		// любом отказе пробуем следующую по старшинству версию сервиса.
		next := fe.SupportedVersion()
		if next == "" || next == version {
			next = prevServiceVersion(version)
		}
		if next == "" {
			return fmt.Errorf("endpoint open (%s): %w", version, err)
		}
		c.tracef("endpoint open: версия %s отклонена (%s), пробуем %s", version, fe.Error(), next)
		version = next
	}
	return fmt.Errorf("не удалось открыть эндпоинт %s: перебраны все версии сервиса", serviceName)
}

// exchange отправляет пакет, тело которого готовит fn, и ждёт пакет wantType.
// Служебные пакеты keep-alive пропускаются, отказы сервера возвращаются как *FailureError.
func (c *conn) exchange(ctx context.Context, pktType byte, fn func(w *rascodec.Writer), wantType byte) ([]byte, error) {
	payload := bytes.Buffer{}
	w := rascodec.NewWriter(&payload)
	fn(w)
	if err := w.Err(); err != nil {
		return nil, err
	}

	frame := bytes.Buffer{}
	fw := rascodec.NewWriter(&frame)
	fw.RawByte(pktType)
	fw.Size(payload.Len())
	fw.Bytes(payload.Bytes())
	if err := fw.Err(); err != nil {
		return nil, err
	}

	c.setDeadline(ctx)
	if _, err := c.tcp.Write(frame.Bytes()); err != nil {
		return nil, err
	}

	for i := 0; i < 32; i++ {
		typ, data, err := c.readPacket()
		if err != nil {
			return nil, err
		}
		switch typ {
		case pktKeepAlive:
			continue
		case wantType:
			return data, nil
		case pktEndpointFailure:
			return nil, parseFailureAck(data)
		case pktDisconnect:
			return nil, errors.New("сервер администрирования 1С разорвал соединение")
		default:
			return nil, fmt.Errorf("неожиданный тип пакета %d (ожидался %d)", typ, wantType)
		}
	}
	return nil, errors.New("сервер администрирования 1С присылает только служебные пакеты")
}

// readPacket читает один кадр: тип (байт), размер (varint), данные.
func (c *conn) readPacket() (byte, []byte, error) {
	head := make([]byte, 1)
	if _, err := io.ReadFull(c.tcp, head); err != nil {
		return 0, nil, err
	}
	sr := rascodec.NewReader(c.tcp)
	size := sr.Size()
	if err := sr.Err(); err != nil {
		return 0, nil, err
	}
	if size < 0 || size > 64*1024*1024 {
		return 0, nil, fmt.Errorf("недопустимый размер пакета RAS: %d", size)
	}
	data := make([]byte, size)
	if size > 0 {
		if _, err := io.ReadFull(c.tcp, data); err != nil {
			return 0, nil, err
		}
	}
	return head[0], data, nil
}

// parseFailureAck разбирает пакет ENDPOINT_FAILURE.
// Порядок полей: serviceId, version, endpointId, classCause, message, trace, cause.
func parseFailureAck(data []byte) error {
	r := rascodec.NewReader(bytes.NewReader(data))
	fe := &FailureError{ServiceID: r.Str(), Version: r.Str()}
	_ = r.Nullable() // идентификатор эндпоинта
	fe.ClassCause = r.Str()
	fe.Message = r.Str()
	if r.Err() == nil && r.Remaining() {
		n := r.Size()
		for i := 0; i < n && i < 64 && r.Err() == nil; i++ {
			fe.Trace = append(fe.Trace, r.Str())
		}
	}
	if r.Err() == nil && r.Remaining() {
		fe.CauseSvc = r.Str()
		fe.CauseMsg = r.Str()
	}
	if r.Err() != nil {
		// Даже при обрыве хвоста текст ошибки уже разобран — возвращаем его.
		return fe
	}
	return fe
}

// invoke выполняет запрос к сервису администрирования и возвращает тело ответа.
// reqType — код сообщения запроса, fill — запись тела запроса (может быть nil).
func (c *conn) invoke(ctx context.Context, reqType int, fill func(w *rascodec.Writer)) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.opened || c.tcp == nil {
		return nil, errors.New("соединение с сервером администрирования 1С закрыто")
	}

	// Тело сообщения сериализуется с учётом согласованной версии эндпоинта:
	// новые версии добавляют поля в конец структур.
	body := bytes.Buffer{}
	bw := rascodec.NewWriter(&body)
	if fill != nil {
		fill(bw)
	}
	if err := bw.Err(); err != nil {
		return nil, err
	}
	endpoint := int(c.endpoint)
	payload := body.Bytes()

	data, err := c.exchange(ctx, pktEndpointMessage, func(w *rascodec.Writer) {
		w.Bytes(endpointEnvelope(endpoint, reqType, payload))
	}, pktEndpointMessage)
	if err != nil {
		c.tracef("запрос %d: ошибка обмена %v", reqType, err)
		return nil, err
	}

	resp, derr := decodeEnvelope(data)
	if derr != nil {
		c.tracef("запрос %d: отказ сервера %v", reqType, derr)
	}
	return resp, derr
}

// endpointEnvelope собирает тело пакета ENDPOINT_MESSAGE:
// идентификатор эндпоинта, формат кодирования, вид нагрузки, код сообщения и само сообщение.
func endpointEnvelope(endpointID, msgType int, body []byte) []byte {
	buf := bytes.Buffer{}
	w := rascodec.NewWriter(&buf)
	w.Nullable(endpointID)
	w.Short(int(rascodec.CodecVersion))
	w.Byte(int(dataMessage))
	w.Byte(msgType)
	w.Bytes(body)
	return buf.Bytes()
}

// decodeEnvelope разбирает ответ сервера: пустой ответ, отказ или тело сообщения.
func decodeEnvelope(data []byte) ([]byte, error) {
	r := rascodec.NewReader(bytes.NewReader(data))
	_ = r.Nullable() // идентификатор эндпоинта
	_ = r.Short()    // формат кодирования
	kind := byte(r.Byte())
	switch kind {
	case dataVoid:
		return nil, nil
	case dataException:
		fe := &FailureError{ServiceID: r.Str(), Message: r.Str()}
		if r.Err() == nil && r.Remaining() {
			fe.CauseSvc = r.Str()
			fe.CauseMsg = r.Str()
		}
		return nil, fe
	case dataMessage:
		_ = r.Byte() // код сообщения-ответа
		return r.BytesRaw(), nil
	default:
		return nil, fmt.Errorf("неизвестный вид ответа сервера администрирования: %d", kind)
	}
}
