package rascodec

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSizeRoundTrip(t *testing.T) {
	values := []int{0, 1, 2, 63, 64, 127, 128, 129, 255, 256, 16383, 16384, 100000, 1 << 21}
	for _, v := range values {
		buf := bytes.Buffer{}
		w := NewWriter(&buf)
		w.Size(v)
		if err := w.Err(); err != nil {
			t.Fatalf("Size(%d) write: %v", v, err)
		}
		r := NewReader(bytes.NewReader(buf.Bytes()))
		got := r.Size()
		if err := r.Err(); err != nil {
			t.Fatalf("Size(%d) read: %v", v, err)
		}
		if got != v {
			t.Errorf("Size: got %d, want %d (байты %x)", got, v, buf.Bytes())
		}
	}
}

func TestSizeGolden(t *testing.T) {
	cases := map[int][]byte{
		0:   {0x00},
		1:   {0x01},
		127: {0x7f},
		128: {0x80, 0x01},
		300: {0xac, 0x02},
	}
	for v, want := range cases {
		buf := bytes.Buffer{}
		NewWriter(&buf).Size(v)
		if !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("Size(%d) = %x, want %x", v, buf.Bytes(), want)
		}
	}
}

func TestNullableRoundTrip(t *testing.T) {
	values := []int{0, 1, 63, 64, 65, 100, 127, 128, 1000, 70000}
	for _, v := range values {
		buf := bytes.Buffer{}
		w := NewWriter(&buf)
		w.Nullable(v)
		if err := w.Err(); err != nil {
			t.Fatalf("Nullable(%d) write: %v", v, err)
		}
		r := NewReader(bytes.NewReader(buf.Bytes()))
		got := r.Nullable()
		if err := r.Err(); err != nil {
			t.Fatalf("Nullable(%d) read: %v", v, err)
		}
		if got != v {
			t.Errorf("Nullable: got %d, want %d (байты %x)", got, v, buf.Bytes())
		}
	}
}

func TestNullableGolden(t *testing.T) {
	// Значение 100: 100 & 0x3F = 36, признак продолжения 0x40, хвост 100>>6 = 1.
	buf := bytes.Buffer{}
	NewWriter(&buf).Nullable(100)
	want := []byte{0x64, 0x01}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("Nullable(100) = %x, want %x", buf.Bytes(), want)
	}
	// Нулевое значение (null) — один нулевой байт.
	buf.Reset()
	NewWriter(&buf).Nullable(0)
	if !bytes.Equal(buf.Bytes(), []byte{0x00}) {
		t.Errorf("Nullable(0) = %x, want 00", buf.Bytes())
	}
}

func TestStringRoundTrip(t *testing.T) {
	values := []string{"", "a", "hello", "Иванова Мария", "Бухгалтерия предприятия (демо)", string(make([]byte, 300))}
	for _, v := range values {
		buf := bytes.Buffer{}
		w := NewWriter(&buf)
		w.Str(v)
		if err := w.Err(); err != nil {
			t.Fatalf("Str write: %v", err)
		}
		r := NewReader(bytes.NewReader(buf.Bytes()))
		got := r.Str()
		if err := r.Err(); err != nil {
			t.Fatalf("Str read: %v", err)
		}
		if got != v {
			t.Errorf("Str: got %q, want %q", got, v)
		}
	}
}

func TestEmptyStringIsNullByte(t *testing.T) {
	buf := bytes.Buffer{}
	NewWriter(&buf).Str("")
	if !bytes.Equal(buf.Bytes(), []byte{0x00}) {
		t.Errorf("пустая строка = %x, want 00", buf.Bytes())
	}
}

func TestStringWithCyrillicLength(t *testing.T) {
	// «привет» — 12 байт в UTF-8, длина кодируется по байтам, а не по символам.
	buf := bytes.Buffer{}
	NewWriter(&buf).Str("привет")
	if buf.Bytes()[0] != 12 {
		t.Errorf("длина строки = %d, want 12 (весь буфер %x)", buf.Bytes()[0], buf.Bytes())
	}
}

func TestNumbersRoundTrip(t *testing.T) {
	buf := bytes.Buffer{}
	w := NewWriter(&buf)
	w.Short(1541)
	w.Int(-7)
	w.Long(1234567890123)
	w.Bool(true)
	w.Bool(false)
	w.Byte(65)
	if err := w.Err(); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := NewReader(bytes.NewReader(buf.Bytes()))
	if got := r.Short(); got != 1541 {
		t.Errorf("Short = %d, want 1541", got)
	}
	if got := int32(r.Int()); got != -7 {
		t.Errorf("Int = %d, want -7", got)
	}
	if got := r.Long(); got != 1234567890123 {
		t.Errorf("Long = %d, want 1234567890123", got)
	}
	if !r.Bool() {
		t.Error("Bool(true) = false")
	}
	if r.Bool() {
		t.Error("Bool(false) = true")
	}
	if got := r.Byte(); got != 65 {
		t.Errorf("Byte = %d, want 65", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestByteNullEncoding(t *testing.T) {
	// Значение 0x80 в эталонном кодеке записывается как 0x00.
	buf := bytes.Buffer{}
	NewWriter(&buf).Byte(0x80)
	if !bytes.Equal(buf.Bytes(), []byte{0x00}) {
		t.Errorf("Byte(0x80) = %x, want 00", buf.Bytes())
	}
}

func TestUUIDRoundTrip(t *testing.T) {
	values := []string{
		"2dbd2cce-fddc-11e8-a982-2c4d544cf050",
		"00000000-0000-0000-0000-000000000000",
		"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
		"",
	}
	for _, v := range values {
		buf := bytes.Buffer{}
		w := NewWriter(&buf)
		w.UUID(v)
		if err := w.Err(); err != nil {
			t.Fatalf("UUID(%q) write: %v", v, err)
		}
		if buf.Len() != 16 {
			t.Fatalf("UUID(%q): записано %d байт, нужно 16", v, buf.Len())
		}
		r := NewReader(bytes.NewReader(buf.Bytes()))
		got := r.UUID()
		if err := r.Err(); err != nil {
			t.Fatalf("UUID(%q) read: %v", v, err)
		}
		want := v
		if want != "" {
			want = strings.ToLower(v)
		}
		if want == "00000000-0000-0000-0000-000000000000" {
			want = ""
		}
		if got != want {
			t.Errorf("UUID: got %q, want %q", got, want)
		}
	}
}

func TestUUIDByteOrder(t *testing.T) {
	// Канонический порядок RFC 4122: старшие байты идут первыми.
	buf := bytes.Buffer{}
	NewWriter(&buf).UUID("01020304-0506-0708-090a-0b0c0d0e0f10")
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("UUID bytes = %x, want %x", buf.Bytes(), want)
	}
}

func TestInvalidUUID(t *testing.T) {
	buf := bytes.Buffer{}
	NewWriter(&buf).UUID("не-uuid")
	// Некорректная строка даёт нулевой идентификатор, а не ошибку записи.
	if buf.Len() != 16 {
		t.Fatalf("записано %d байт, нужно 16", buf.Len())
	}
	for _, b := range buf.Bytes() {
		if b != 0 {
			t.Fatalf("ожидались нули, получено %x", buf.Bytes())
		}
	}
	if _, err := ParseUUID("zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"); err == nil {
		t.Error("ожидалась ошибка разбора некорректного UUID")
	}
}

func TestTimeRoundTrip(t *testing.T) {
	values := []time.Time{
		time.Time{},
		time.Date(2026, 3, 15, 12, 34, 56, 0, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Now().UTC().Truncate(time.Millisecond),
	}
	for _, v := range values {
		buf := bytes.Buffer{}
		w := NewWriter(&buf)
		w.Time(v)
		if err := w.Err(); err != nil {
			t.Fatalf("Time write: %v", err)
		}
		if buf.Len() != 8 {
			t.Fatalf("Time: записано %d байт, нужно 8", buf.Len())
		}
		r := NewReader(bytes.NewReader(buf.Bytes()))
		got := r.Time()
		if err := r.Err(); err != nil {
			t.Fatalf("Time read: %v", err)
		}
		if v.IsZero() {
			if !got.IsZero() {
				t.Errorf("нулевое время прочиталось как %v", got)
			}
			continue
		}
		if !got.Equal(v) {
			t.Errorf("Time: got %v, want %v", got, v)
		}
	}
}

func TestTimeTicksGolden(t *testing.T) {
	// 1970-01-01 00:00:00 UTC в тиках 1С — ровно AgeDelta.
	buf := bytes.Buffer{}
	NewWriter(&buf).Time(time.Unix(0, 0).UTC())
	r := NewReader(bytes.NewReader(buf.Bytes()))
	if got := r.Long(); got != AgeDelta {
		t.Errorf("тики эпохи = %d, want %d", got, AgeDelta)
	}
}

func TestNegotiateGolden(t *testing.T) {
	// Первое сообщение рукопожатия: magic + версия протокола + версия кодека.
	buf := bytes.Buffer{}
	w := NewWriter(&buf)
	w.Int(475223888)
	w.Short(256)
	w.Short(256)
	want := []byte{0x1c, 0x53, 0x57, 0x50, 0x01, 0x00, 0x01, 0x00}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("negotiate = %x, want %x", buf.Bytes(), want)
	}
}

func TestTruncatedInput(t *testing.T) {
	r := NewReader(bytes.NewReader([]byte{0x01, 0x02}))
	_ = r.Long()
	if r.Err() == nil {
		t.Error("ожидалась ошибка на оборванных данных")
	}
}

func TestRemaining(t *testing.T) {
	buf := bytes.Buffer{}
	w := NewWriter(&buf)
	w.Str("a")
	w.Str("b")
	r := NewReader(bytes.NewReader(buf.Bytes()))
	if !r.Remaining() {
		t.Error("до чтения Remaining должен быть true")
	}
	_ = r.Str()
	if !r.Remaining() {
		t.Error("после первой строки должны остаться данные")
	}
	_ = r.Str()
	if r.Remaining() {
		t.Error("после второй строки данных остаться не должно")
	}
}


