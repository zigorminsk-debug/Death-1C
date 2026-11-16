// Package rascodec реализует двоичный кодек протокола RAS 1С:Предприятие 8.3.
//
// Это собственная реализация формата кодирования (так называемый «codec256»),
// написанная только на стандартной библиотеке Go: без protobuf и сторонних
// зависимостей. Формат совпадает с тем, который используют утилита rac.exe
// и сервер администрирования ras.exe (порт 1545).
//
// Порядок полей в сообщениях сверен с двумя независимыми реализациями
// протокола, поэтому сериализация байт-в-байт соответствует эталонной.
package rascodec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// Константы формата. Значения совпадают с эталонным кодеком 1С.
const (
	sizeOfShort = 2
	sizeOfInt   = 4
	sizeOfLong  = 8

	nullByte     = 0x80
	trueByte     = 1
	falseByte    = 0
	maxShift     = 7
	nullShift    = 6
	nextMask     = 0x80
	nullNextMask = 0x40
	low7Mask     = 0x7F
	low6Mask     = 0x3F

	// AgeDelta — смещение между «тиками» 1С (100 нс от 0001-01-01) и Unix-временем.
	AgeDelta int64 = 621355968000000

	// CodecVersion — версия формата кодирования (поле Format в сообщениях эндпоинта).
	CodecVersion int32 = 256
)

// ErrTruncated — ответ сервера оборвался раньше, чем ожидалось.
var ErrTruncated = errors.New("ras: ответ сервера оборван")

// Writer — накопитель ошибок при сериализации: любая ошибка запоминается,
// последующие вызовы становятся пустыми операциями, а Err() возвращает первую.
type Writer struct {
	w   io.Writer
	err error
}

// NewWriter создаёт писатель поверх io.Writer.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Err возвращает первую ошибку сериализации, если она была.
func (x *Writer) Err() error { return x.err }

func (x *Writer) write(b []byte) {
	if x.err != nil {
		return
	}
	if _, err := x.w.Write(b); err != nil {
		x.err = err
	}
}

func (x *Writer) writeByte(b byte) { x.write([]byte{b}) }

// Byte пишет один байт. Значение 0x80 кодируется как 0x00 — так же,
// как это делает эталонный кодек.
func (x *Writer) Byte(v int) {
	b := byte(v)
	if b == nullByte {
		b = 0
	}
	x.writeByte(b)
}

// RawByte пишет байт без преобразования (используется для типов пакетов).
func (x *Writer) RawByte(v byte) { x.writeByte(v) }

// Bool пишет логическое значение одним байтом.
func (x *Writer) Bool(v bool) {
	if v {
		x.writeByte(trueByte)
		return
	}
	x.writeByte(falseByte)
}

// Short пишет 16-битное целое в порядке big-endian.
func (x *Writer) Short(v int) {
	buf := make([]byte, sizeOfShort)
	binary.BigEndian.PutUint16(buf, uint16(v))
	x.write(buf)
}

// Int пишет 32-битное целое в порядке big-endian.
func (x *Writer) Int(v int) {
	buf := make([]byte, sizeOfInt)
	binary.BigEndian.PutUint32(buf, uint32(v))
	x.write(buf)
}

// Long пишет 64-битное целое в порядке big-endian.
func (x *Writer) Long(v int64) {
	buf := make([]byte, sizeOfLong)
	binary.BigEndian.PutUint64(buf, uint64(v))
	x.write(buf)
}

// Float пишет 32-битное число с плавающей точкой.
func (x *Writer) Float(v float32) { x.Int(int(math.Float32bits(v))) }

// Double пишет 64-битное число с плавающей точкой.
func (x *Writer) Double(v float64) { x.Long(int64(math.Float64bits(v))) }

// Size пишет размер в формате varint (7 бит на байт, старший бит — «продолжение»).
func (x *Writer) Size(v int) {
	msb := v >> maxShift
	first := 0
	if msb != 0 {
		first = nextMask
	}
	x.writeByte(byte(first | (v & low7Mask)))
	for val := msb; val > 0; val = msb {
		msb >>= maxShift
		mask := 0
		if msb != 0 {
			mask = nextMask
		}
		x.writeByte(byte(mask | (val & low7Mask)))
	}
}

// Nullable пишет «обнуляемый» размер: 6 бит данных в первом байте,
// бит 0x40 — признак продолжения, 0x80 — признак null.
func (x *Writer) Nullable(v int) {
	msb := v >> nullShift
	first := 0
	if msb != 0 {
		first = nullNextMask
	}
	x.writeByte(byte(first | (v & low7Mask)))
	for val := msb; val > 0; val = msb {
		msb >>= maxShift
		mask := 0
		if msb != 0 {
			mask = nextMask
		}
		x.writeByte(byte(mask | (val & low7Mask)))
	}
}

// Str пишет строку в UTF-8: пустая строка — один нулевой байт,
// иначе обнуляемая длина и сами байты.
func (x *Writer) Str(s string) {
	if len(s) == 0 {
		x.writeByte(0)
		return
	}
	b := []byte(s)
	x.Nullable(len(b))
	x.write(b)
}

// Bytes пишет «сырые» байты без длины (длина известна из контекста).
func (x *Writer) Bytes(b []byte) { x.write(b) }

// UUID пишет 16 байт идентификатора в каноническом порядке (RFC 4122).
// Пустая или неразобранная строка даёт нулевой UUID.
func (x *Writer) UUID(s string) {
	var buf [16]byte
	if b, err := ParseUUID(s); err == nil {
		buf = b
	}
	x.write(buf[:])
}

// Time пишет время как число тиков 1С (100-наносекундных интервалов от 0001-01-01).
func (x *Writer) Time(t time.Time) {
	if t.IsZero() {
		x.Long(0)
		return
	}
	ticks := t.UnixNano()/int64(time.Millisecond)*10 + AgeDelta
	x.Long(ticks)
}

// Reader — накопитель ошибок при разборе ответа сервера.
type Reader struct {
	r   io.Reader
	err error
}

// NewReader создаёт читатель поверх io.Reader.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Err возвращает первую ошибку разбора, если она была.
func (x *Reader) Err() error { return x.err }

// Remaining сообщает, остались ли в источнике нерасшифрованные байты.
// Работает только для источников, поддерживающих peek (bytes.Reader),
// иначе всегда возвращает false.
func (x *Reader) Remaining() bool {
	if x.err != nil {
		return false
	}
	if br, ok := x.r.(interface{ Len() int }); ok {
		return br.Len() > 0
	}
	return false
}

func (x *Reader) readN(n int) []byte {
	if x.err != nil {
		return nil
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf
	}
	if _, err := io.ReadFull(x.r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			x.err = fmt.Errorf("%w: нужно %d байт", ErrTruncated, n)
		} else {
			x.err = err
		}
		return nil
	}
	return buf
}

func (x *Reader) readByte() byte {
	b := x.readN(1)
	if b == nil {
		return 0
	}
	return b[0]
}

// Byte читает один байт.
func (x *Reader) Byte() int { return int(x.readByte()) }

// Bool читает логическое значение.
func (x *Reader) Bool() bool { return x.readByte() == trueByte }

// Short читает 16-битное целое.
func (x *Reader) Short() int {
	b := x.readN(sizeOfShort)
	if b == nil {
		return 0
	}
	return int(binary.BigEndian.Uint16(b))
}

// Int читает 32-битное целое.
func (x *Reader) Int() int {
	b := x.readN(sizeOfInt)
	if b == nil {
		return 0
	}
	return int(binary.BigEndian.Uint32(b))
}

// Long читает 64-битное целое.
func (x *Reader) Long() int64 {
	b := x.readN(sizeOfLong)
	if b == nil {
		return 0
	}
	return int64(binary.BigEndian.Uint64(b))
}

// Float читает 32-битное число с плавающей точкой.
func (x *Reader) Float() float32 { return math.Float32frombits(uint32(x.Int())) }

// Double читает 64-битное число с плавающей точкой.
func (x *Reader) Double() float64 { return math.Float64frombits(uint64(x.Long())) }

// Size читает размер в формате varint.
func (x *Reader) Size() int {
	cur := int(x.readByte())
	if x.err != nil {
		return 0
	}
	size := cur & low7Mask
	for shift := maxShift; cur >= 0x80; shift += maxShift {
		cur = int(x.readByte())
		if x.err != nil {
			return 0
		}
		size += (cur & low7Mask) << shift
		if shift > 56 {
			x.err = errors.New("ras: размер varint слишком велик")
			return 0
		}
	}
	return size
}

// Nullable читает «обнуляемый» размер.
func (x *Reader) Nullable() int {
	cur := int(x.readByte())
	if x.err != nil {
		return 0
	}
	if cur < 0x80 {
		size := cur & low6Mask
		if cur&nullNextMask == 0 {
			return size
		}
		shift := nullShift
		for {
			cur = int(x.readByte())
			if x.err != nil {
				return 0
			}
			size += (cur & low7Mask) << shift
			shift += maxShift
			if cur < 0x80 {
				return size
			}
			if shift > 56 {
				x.err = errors.New("ras: обнуляемый размер слишком велик")
				return 0
			}
		}
	}
	if cur&low7Mask != 0 {
		x.err = errors.New("ras: ожидался null")
		return 0
	}
	return 0
}

// Str читает строку UTF-8.
func (x *Reader) Str() string {
	size := x.Nullable()
	if x.err != nil {
		return ""
	}
	if size == 0 {
		return ""
	}
	if size > 16*1024*1024 {
		x.err = fmt.Errorf("ras: строка подозрительно длинная (%d байт)", size)
		return ""
	}
	b := x.readN(size)
	if b == nil {
		return ""
	}
	return string(b)
}

// BytesRaw читает всё, что осталось в источнике.
func (x *Reader) BytesRaw() []byte {
	if x.err != nil {
		return nil
	}
	b, err := io.ReadAll(x.r)
	if err != nil {
		x.err = err
		return nil
	}
	return b
}

// Bytes читает ровно n байт.
func (x *Reader) Bytes(n int) []byte { return x.readN(n) }

// UUID читает 16 байт идентификатора и возвращает его канонической строкой.
func (x *Reader) UUID() string {
	b := x.readN(16)
	if b == nil {
		return ""
	}
	var arr [16]byte
	copy(arr[:], b)
	return FormatUUID(arr)
}

// Time читает время из числа тиков 1С.
func (x *Reader) Time() time.Time {
	ticks := x.Long()
	if ticks <= 0 {
		return time.Time{}
	}
	ms := (ticks - AgeDelta) / 10
	return time.Unix(0, ms*int64(time.Millisecond))
}

// ParseUUID разбирает каноническую строку UUID (с дефисами или без) в 16 байт.
func ParseUUID(s string) ([16]byte, error) {
	var out [16]byte
	if s == "" {
		return out, nil
	}
	hexDigits := make([]byte, 0, 32)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			continue
		}
		v, ok := hexVal(c)
		if !ok {
			return out, fmt.Errorf("ras: некорректный UUID %q", s)
		}
		hexDigits = append(hexDigits, v)
	}
	if len(hexDigits) != 32 {
		return out, fmt.Errorf("ras: некорректная длина UUID %q", s)
	}
	for i := 0; i < 16; i++ {
		out[i] = hexDigits[i*2]<<4 | hexDigits[i*2+1]
	}
	return out, nil
}

// FormatUUID возвращает каноническую строку UUID из 16 байт.
// Нулевой идентификатор представляется пустой строкой — так удобнее в интерфейсе.
func FormatUUID(b [16]byte) string {
	const hex = "0123456789abcdef"
	zero := true
	for _, v := range b {
		if v != 0 {
			zero = false
			break
		}
	}
	if zero {
		return ""
	}
	out := make([]byte, 0, 36)
	for i, v := range b {
		switch i {
		case 4, 6, 8, 10:
			out = append(out, '-')
		}
		out = append(out, hex[v>>4], hex[v&0x0F])
	}
	return string(out)
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
