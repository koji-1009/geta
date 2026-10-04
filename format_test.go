package geta_test

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json/v2"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/koji-1009/geta"
	"github.com/koji-1009/geta/getaclient"
	"github.com/koji-1009/geta/getatest"
)

// formatCase lists texts a format type reads and texts it refuses, from
// the grammar its specification gives.
type formatCase struct {
	format  string
	read    func(string) (fmt.Stringer, error)
	accept  []string
	refuse  []string
	written map[string]string // a text the type writes otherwise than it was read
}

func reader[T fmt.Stringer](parse func(string) (T, error)) func(string) (fmt.Stringer, error) {
	return func(s string) (fmt.Stringer, error) { return parse(s) }
}

var formatCases = []formatCase{
	{format: "date-time", read: reader(geta.ParseDateTime),
		// RFC 3339 section 5.6 date-time: full-date "T" full-time, T and Z in
		// either case; second 60 only at 23:59 UTC (section 5.7).
		accept: []string{"2016-12-31T23:59:60Z", "2016-12-31t15:59:60.123-08:00", "0000-01-01T00:00:00Z",
			"9999-12-31T23:59:59.999999999999999+23:59", "2024-02-29T12:00:00-00:00", "2017-01-01T08:59:60+09:00"},
		refuse: []string{"", "2016-12-31T23:58:60Z", "2016-12-31T22:59:60Z", "2016-12-31T23:59:61Z", "2023-02-29T00:00:00Z",
			"2016-12-31 23:59:60Z", "2016-12-31T23:59:60", "2016-12-31T24:00:00Z", "2016-12-31T23:59:60+24:00",
			"+2016-12-31T23:59:60Z", "2016-12-31T23:59:60Z\n", "2016-12-31"},
		written: map[string]string{"2016-12-31t15:59:60.123-08:00": "2016-12-31T15:59:60.123-08:00"}},
	{format: "date", read: reader(geta.ParseDate),
		// RFC 3339 section 5.6 full-date, with the days of section 5.7 and the
		// leap years of Appendix C (0000 and 2000 are leap years, 1900 is not).
		accept: []string{"2024-02-29", "2000-02-29", "0000-02-29", "9999-12-31", "0001-01-01", "2023-04-30"},
		refuse: []string{"", "1900-02-29", "2023-02-29", "2023-04-31", "2023-13-01", "2023-00-10", "2023-01-00",
			"23-01-01", "2023-1-01", "2023-01-01T00:00:00Z", "2023/01/01", "２０２３-01-01", "+2023-01-01", " 2023-01-01"}},
	{format: "time", read: reader(geta.ParseTimeOfDay),
		// RFC 3339 section 5.6 full-time: an offset is required, T and Z in
		// either case, a fraction of any length; second 60 only at 23:59 UTC
		// (section 5.7).
		accept: []string{"00:00:00Z", "23:59:59.999999999999999z", "23:59:60Z", "00:29:60+00:30", "15:59:60-08:00",
			"12:00:00-00:00", "12:00:00+23:59", "12:00:00.5+00:00"},
		refuse: []string{"", "24:00:00Z", "12:60:00Z", "12:00:61Z", "12:00:00", "12:00Z", "12:00:00.Z",
			"12:00:00+24:00", "12:00:00+00:60", "12:00:00 Z", "23:59:60+01:00", "22:59:60Z", "12:00:00,5Z",
			"12:00:00+0100", "T12:00:00Z", "1:00:00Z"},
		written: map[string]string{"23:59:59.999999999999999z": "23:59:59.999999999999999Z"}},
	{format: "duration", read: reader(geta.ParseDuration),
		// RFC 3339 Appendix A: components in order with no gap, a week alone,
		// whole numbers of any length; ABNF's letters match either case.
		accept: []string{"P1D", "p1d", "PT0S", "P0D", "P1Y2M", "P1M2D", "PT1M2S", "PT36H", "P2W", "P01D",
			"P1Y2M3DT4H5M6S", "P99999999999999999999999D"},
		refuse: []string{"", "P", "PT", "P1YT", "P1Y2D", "PT1H2S", "P1W1D", "P1WT1H", "P1.5D", "PT0,5S",
			"-P1D", "P-1D", "P1D2H", "P2S", "PT1D", "1D", "P1", "P1D "}},
	{format: "ipv4", read: reader(geta.ParseIPv4),
		// RFC 2673 section 3.2 dotted-quad: decbyte = 1*3DIGIT up to 255.
		accept: []string{"0.0.0.0", "255.255.255.255", "192.0.2.1", "010.0.0.1", "000.000.000.000"},
		refuse: []string{"", "256.0.0.1", "1.2.3", "1.2.3.4.5", "0x7f.0.0.1", "1.2.3.0010", "1.2.3.-1", "1..2.3",
			"1.2.3.4/8", " 1.2.3.4", "::ffff:1.2.3.4", "1.2.3.٤"},
		written: map[string]string{"010.0.0.1": "10.0.0.1", "000.000.000.000": "0.0.0.0"}},
	{format: "ipv6", read: reader(geta.ParseIPv6),
		// RFC 4291 section 2.2, by RFC 3986's IPv6address.
		accept: []string{"::", "::1", "1:2:3:4:5:6:7:8", "1:2:3:4:5:6:7::", "::2:3:4:5:6:7:8", "1::", "FFFF::",
			"::ffff:1.2.3.4", "1:2:3:4:5:6:1.2.3.4", "1::1.2.3.4", "0001:0:0:0:0:0:0:1"},
		refuse: []string{"", "1:2:3:4:5:6:7:8:9", "1::2::3", "1:2:3:4:5:6:7:1.2.3.4", "1:2:3:4:5:6::1.2.3.4",
			"::1.2.3.04", "fe80::1%eth0", "12345::", "1:2:3:4:5:6:7", ":1::", "1:::2", "::1.2.3.4:1", "::1.2.3",
			"[::1]", "1.2.3.4"},
		// Written as RFC 5952 recommends: lower case, no leading zeros, the
		// longest run of two or more zero groups as "::", and dotted decimal
		// only for an IPv4-mapped address.
		written: map[string]string{"FFFF::": "ffff::", "0001:0:0:0:0:0:0:1": "1::1", "1:2:3:4:5:6:7::": "1:2:3:4:5:6:7:0",
			"::2:3:4:5:6:7:8": "0:2:3:4:5:6:7:8", "1:2:3:4:5:6:1.2.3.4": "1:2:3:4:5:6:102:304", "1::1.2.3.4": "1::102:304"}},
	{format: "json-pointer", read: reader(geta.ParseJSONPointer),
		// RFC 6901 section 3.
		accept: []string{"", "/", "/a~0~1", "//", "/a b/é/\x00"},
		refuse: []string{"a", "/~", "/~2", "#/a", "/a~"}},
	{format: "relative-json-pointer", read: reader(geta.ParseRelativeJSONPointer),
		// draft-handrews-relative-json-pointer-01 section 3.
		accept: []string{"0", "0#", "10/a", "0//", "99999999999999999999999"},
		refuse: []string{"", "01", "-1", "+1", "1#/a", "0##", "/a", "1a", "0+1/a"}},
}

// Each format type reads what its grammar holds, refuses the rest, and
// writes what it read.
func TestFormatTypesReadTheirGrammars(t *testing.T) {
	for _, fc := range formatCases {
		for _, s := range fc.accept {
			v, err := fc.read(s)
			if err != nil {
				t.Errorf("%s: refused %q: %v", fc.format, s, err)
				continue
			}
			want := s
			if w, ok := fc.written[s]; ok {
				want = w
			}
			if got := v.String(); got != want {
				t.Errorf("%s: %q written as %q, want %q", fc.format, s, got, want)
			}
			// MarshalText writes the same, and UnmarshalText reads it back
			// to an equal value.
			b, err := v.(encoding.TextMarshaler).MarshalText()
			if err != nil || string(b) != want {
				t.Errorf("%s: MarshalText of %q: %q, %v", fc.format, s, b, err)
			}
			back := reflect.New(reflect.TypeOf(v))
			if err := back.Interface().(encoding.TextUnmarshaler).UnmarshalText(b); err != nil {
				t.Errorf("%s: re-reading %q: %v", fc.format, b, err)
			}
			if w, ok := fc.written[s]; !ok || w == s {
				if !reflect.DeepEqual(back.Elem().Interface(), v) {
					t.Errorf("%s: %q read back as %#v, want %#v", fc.format, b, back.Elem().Interface(), v)
				}
			}
		}
		for _, s := range fc.refuse {
			if v, err := fc.read(s); err == nil {
				t.Errorf("%s: accepted %q as %v", fc.format, s, v)
			} else if !strings.Contains(err.Error(), "not a valid "+fc.format) {
				t.Errorf("%s: error %v", fc.format, err)
			}
		}
	}
}

// The zero value of each format type writes something; for those with a
// valid zero value, that value.
func TestFormatTypesZeroValues(t *testing.T) {
	for v, want := range map[encoding.TextMarshaler]string{
		geta.Date{}: "0000-01-01", geta.TimeOfDay{}: "00:00:00Z", geta.Duration{}: "PT0S",
		geta.DateTime{}: "0000-01-01T00:00:00Z", geta.IPv4{}: "0.0.0.0", geta.IPv6{}: "::", geta.JSONPointer{}: "",
		// No relative pointer is empty: an unset one writes "", which
		// getatest's Conforms reports.
		geta.RelativeJSONPointer{}: "",
	} {
		b, err := v.MarshalText()
		if err != nil || string(b) != want {
			t.Errorf("%T{}: %q, %v; want %q", v, b, err, want)
		}
	}
}

func TestDateAndTimeOfDay(t *testing.T) {
	d, err := geta.NewDate(2024, time.February, 29)
	if err != nil || d.String() != "2024-02-29" || d.Year() != 2024 || d.Month() != time.February || d.Day() != 29 {
		t.Fatal(d, err)
	}
	if !d.In(time.UTC).Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(d.In(time.UTC))
	}
	for _, bad := range [][3]int{{2023, 2, 29}, {10000, 1, 1}, {-1, 1, 1}, {2023, 13, 1}, {2023, 1, 0}} {
		if _, err := geta.NewDate(bad[0], time.Month(bad[1]), bad[2]); err == nil {
			t.Errorf("NewDate%v accepted", bad)
		}
	}
	if d, err := geta.DateOf(time.Date(2026, 7, 18, 23, 0, 0, 0, time.FixedZone("", 9*3600))); err != nil || d.String() != "2026-07-18" {
		t.Fatal(d, err)
	}
	if _, err := geta.DateOf(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("year 10000 accepted")
	}

	tm, err := geta.ParseTimeOfDay("08:59:60.1234567896-09:30")
	if err == nil {
		t.Fatalf("leap second at 18:29 UTC accepted: %v", tm)
	}
	tm, err = geta.ParseTimeOfDay("08:30:05.1234567896-09:30")
	if err != nil || tm.Hour() != 8 || tm.Minute() != 30 || tm.Second() != 5 || tm.Nanosecond() != 123456789 ||
		tm.Offset() != -(9*3600+30*60) || tm.UnknownOffset() {
		t.Fatal(tm, err)
	}
	at, err := tm.On(d)
	if err != nil || !at.Equal(time.Date(2024, 2, 29, 18, 0, 5, 123456789, time.UTC)) {
		t.Fatal(at, err)
	}
	unknown, _ := geta.ParseTimeOfDay("12:00:00-00:00")
	if !unknown.UnknownOffset() || unknown.Offset() != 0 || unknown.String() != "12:00:00-00:00" {
		t.Fatal(unknown)
	}
	leap, _ := geta.ParseTimeOfDay("23:59:60Z")
	if _, err := leap.On(d); err == nil {
		t.Fatal("a leap second became a time.Time")
	}
	of, err := geta.TimeOfDayOf(time.Date(2026, 1, 1, 9, 5, 7, 500, time.FixedZone("", 5*3600+45*60)))
	if err != nil || of.String() != "09:05:07.0000005+05:45" {
		t.Fatal(of, err)
	}
	if of, err := geta.TimeOfDayOf(time.Date(2026, 1, 1, 9, 5, 7, 0, time.UTC)); err != nil || of.String() != "09:05:07Z" {
		t.Fatal(of, err)
	}
	// An offset in seconds, as some historical zones have, is not RFC 3339's.
	if _, err := geta.NewTimeOfDay(1, 2, 3, 0, 61); err == nil {
		t.Fatal("offset of 61s accepted")
	}
	if _, err := geta.NewTimeOfDay(22, 59, 60, 0, 0); err == nil {
		t.Fatal("leap second at 22:59 accepted")
	}
	if v, err := geta.NewTimeOfDay(8, 59, 60, 0, 9*3600); err != nil || v.String() != "08:59:60+09:00" {
		t.Fatal(v, err)
	}
}

// A DateTime holds a leap second; as a time.Time it reads as second 0 of
// the next minute.
func TestDateTime(t *testing.T) {
	dt, err := geta.ParseDateTime("2016-12-31T15:59:60.5-08:00")
	if err != nil || !dt.LeapSecond() || dt.Date().String() != "2016-12-31" || dt.TimeOfDay().String() != "15:59:60.5-08:00" {
		t.Fatal(dt, err)
	}
	if got := dt.Time(); !got.Equal(time.Date(2017, 1, 1, 0, 0, 0, 5e8, time.UTC)) {
		t.Fatal(got)
	}
	if _, off := dt.Time().Zone(); off != -8*3600 {
		t.Fatal(off)
	}
	d, _ := geta.NewDate(2026, time.July, 18)
	tod, _ := geta.NewTimeOfDay(9, 30, 0, 0, 9*3600)
	plain := geta.NewDateTime(d, tod)
	if plain.LeapSecond() || plain.String() != "2026-07-18T09:30:00+09:00" ||
		!plain.Time().Equal(time.Date(2026, 7, 18, 0, 30, 0, 0, time.UTC)) {
		t.Fatal(plain, plain.Time())
	}
	at := time.Date(2026, 7, 18, 9, 30, 0, 0, time.FixedZone("", 9*3600))
	if of, err := geta.DateTimeOf(at); err != nil || of != plain {
		t.Fatal(of, err)
	}
	if _, err := geta.DateTimeOf(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("year 10000 accepted")
	}
	if _, err := geta.DateTimeOf(time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 61))); err == nil {
		t.Fatal("offset of 61s accepted")
	}
}

type leapEnum struct {
	Stamp time.Time `json:"stamp" schema:"enum=2016-12-31T23:59:59Z|2016-12-31T23:59:60Z"`
}

type leapEnumDateTime struct {
	Instant geta.DateTime `json:"instant" schema:"enum=2016-12-31T23:59:60Z"`
}

// An enum member a time.Time does not hold would be documented and refused:
// geta.New refuses it. A DateTime holds it.
func TestLeapSecondEnumOfTimeIsRefused(t *testing.T) {
	rejects(t, one("/x", get(func(context.Context, *empty) (*leapEnum, error) { return nil, nil })),
		`leapEnum.Stamp: enum member "2016-12-31T23:59:60Z" is a leap second, which a time.Time does not hold`)
	accepts(t, one("/x", get(func(context.Context, *empty) (*leapEnumDateTime, error) { return nil, nil })))
}

func TestDuration(t *testing.T) {
	d, _ := geta.ParseDuration("P1Y2M3DT4H5M6S")
	p, err := d.Parts()
	if err != nil || p != (geta.DurationParts{Years: 1, Months: 2, Days: 3, Hours: 4, Minutes: 5, Seconds: 6}) {
		t.Fatal(p, err)
	}
	if w, _ := geta.ParseDuration("p2w"); func() bool { p, _ := w.Parts(); return p.Weeks != 2 }() {
		t.Fatal(w)
	}
	if _, err := d.Std(); err == nil {
		t.Fatal("calendar components became a time.Duration")
	}
	clock, _ := geta.ParseDuration("PT1H30M")
	if std, err := clock.Std(); err != nil || std != 90*time.Minute {
		t.Fatal(std, err)
	}
	if std, err := (geta.Duration{}).Std(); err != nil || std != 0 {
		t.Fatal(std, err)
	}
	month, _ := geta.ParseDuration("P1MT1H")
	if at, err := month.AddTo(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)); err != nil || !at.Equal(time.Date(2026, 2, 15, 1, 0, 0, 0, time.UTC)) {
		t.Fatal(at, err)
	}
	huge, _ := geta.ParseDuration("P99999999999999999999999D")
	if _, err := huge.Parts(); err == nil {
		t.Fatal("a component past uint64 was read")
	}
	if _, err := huge.AddTo(time.Now()); err == nil {
		t.Fatal("added")
	}
	big, _ := geta.ParseDuration("PT9999999999H")
	if _, err := big.Std(); err == nil {
		t.Fatal("a duration past time.Duration was converted")
	}
	for in, want := range map[time.Duration]string{0: "PT0S", 3605 * time.Second: "PT1H0M5S", 2 * time.Hour: "PT2H",
		90 * time.Second: "PT1M30S", time.Minute: "PT1M", 59 * time.Second: "PT59S"} {
		d, err := geta.DurationOf(in)
		if err != nil || d.String() != want {
			t.Errorf("DurationOf(%s) = %v, %v; want %s", in, d, err, want)
		}
		if back, err := d.Std(); err != nil || back != in {
			t.Errorf("%s back as %s, %v", d, back, err)
		}
	}
	for _, bad := range []time.Duration{-time.Second, 1500 * time.Millisecond} {
		if _, err := geta.DurationOf(bad); err == nil {
			t.Errorf("DurationOf(%s) accepted", bad)
		}
	}
}

func TestNetworkFormats(t *testing.T) {
	v4, _ := geta.ParseIPv4("192.000.002.001")
	if v4.Addr() != netip.MustParseAddr("192.0.2.1") {
		t.Fatal(v4)
	}
	if got, err := geta.IPv4Of(netip.MustParseAddr("192.0.2.1")); err != nil || got != v4 {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"::1", "::ffff:1.2.3.4"} {
		if _, err := geta.IPv4Of(netip.MustParseAddr(bad)); err == nil {
			t.Errorf("IPv4Of(%s) accepted", bad)
		}
	}
	v6, _ := geta.ParseIPv6("2001:db8:0:0::1")
	if v6.Addr() != netip.MustParseAddr("2001:db8::1") || v6.String() != "2001:db8::1" {
		t.Fatal(v6)
	}
	if got, err := geta.IPv6Of(netip.MustParseAddr("::ffff:1.2.3.4")); err != nil || got.String() != "::ffff:1.2.3.4" {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"1.2.3.4", "fe80::1%eth0"} {
		if _, err := geta.IPv6Of(netip.MustParseAddr(bad)); err == nil {
			t.Errorf("IPv6Of(%s) accepted", bad)
		}
	}
}

func TestPointerFormats(t *testing.T) {
	p := geta.NewJSONPointer("a/b", "~", "")
	if p.String() != "/a~1b/~0/" || !slices.Equal(p.Tokens(), []string{"a/b", "~", ""}) {
		t.Fatal(p, p.Tokens())
	}
	if (geta.JSONPointer{}).Tokens() != nil || geta.NewJSONPointer().String() != "" {
		t.Fatal("the whole document")
	}
	if q, _ := geta.ParseJSONPointer("/~01"); !slices.Equal(q.Tokens(), []string{"~1"}) {
		t.Fatal(q.Tokens())
	}
	rp, _ := geta.ParseRelativeJSONPointer("12/a~1b")
	if n, ok := rp.Up(); n != 12 || !ok || rp.Hash() || rp.Pointer().String() != "/a~1b" {
		t.Fatal(rp)
	}
	rh, _ := geta.ParseRelativeJSONPointer("3#")
	if n, ok := rh.Up(); n != 3 || !ok || !rh.Hash() || rh.Pointer().String() != "" {
		t.Fatal(rh)
	}
	rb, _ := geta.ParseRelativeJSONPointer("99999999999999999999999")
	if _, ok := rb.Up(); ok {
		t.Fatal("a number past uint64 was read")
	}
}

// fmt and log/slog write a Password as [redacted]; JSON writes it in full.
func TestPasswordIsRedacted(t *testing.T) {
	type form struct {
		User string        `json:"user"`
		Pass geta.Password `json:"pass"`
	}
	p := geta.Password("hunter2")
	f := form{User: "u", Pass: p}
	for _, s := range []string{fmt.Sprint(p), fmt.Sprintf("%v %s %q %x %d %#v %+v", p, p, p, p, p, p, f), fmt.Sprintf("%#v", f)} {
		if strings.Contains(s, "hunter2") || strings.Contains(s, "68756e74657232") {
			t.Errorf("leaked: %s", s)
		}
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "p", p)
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "p", p)
	if strings.Contains(buf.String(), "hunter2") {
		t.Errorf("leaked: %s", buf.String())
	}
	b, err := json.Marshal(f)
	if err != nil || string(b) != `{"user":"u","pass":"hunter2"}` {
		t.Fatal(string(b), err)
	}
}

// Every format type, in each place a value is bound and written.
type formatBody struct {
	Date     geta.Date                `json:"date"`
	Time     geta.TimeOfDay           `json:"time"`
	Duration geta.Duration            `json:"duration"`
	V4       geta.IPv4                `json:"v4"`
	V6       geta.IPv6                `json:"v6" schema:"maxLength=20"`
	Pointer  geta.JSONPointer         `json:"pointer"`
	Relative geta.RelativeJSONPointer `json:"relative"`
	Secret   geta.Password            `json:"secret" schema:"minLength=8"`
	Stamp    time.Time                `json:"stamp"`
	Instant  geta.DateTime            `json:"instant"`
	Days     []geta.Date              `json:"days" schema:"uniqueItems=true"`
	ByName   map[string]geta.IPv4     `json:"byName"`
	Maybe    *geta.IPv6               `json:"maybe,omitzero"`
}

type formatIn struct {
	Day      geta.Date                 `path:"day"`
	At       *geta.TimeOfDay           `query:"at"`
	Since    *geta.DateTime            `header:"X-Since"`
	For      *geta.Duration            `query:"for"`
	Addrs    []geta.IPv4               `query:"addr"`
	Client   *geta.IPv4                `header:"X-Client"`
	Origin   *geta.IPv6                `header:"X-Origin"`
	Path     *geta.JSONPointer         `cookie:"path"`
	Pointer  *geta.JSONPointer         `query:"pointer"`
	Relative *geta.RelativeJSONPointer `query:"relative"`
	Secret   *geta.Password            `header:"X-Secret"`
	Body     *formatBody               `body:"json"`
}

type formatOut struct {
	Next  geta.Date      `header:"X-Next"`
	First geta.IPv4      `header:"X-First"`
	Wait  *geta.Duration `header:"X-Wait"`
	Body  formatBody     `body:"json"`
}

func formatApp(t *testing.T, got **formatIn) geta.Table {
	t.Helper()
	h := func(ctx context.Context, in *formatIn) (*formatOut, error) {
		*got = in
		out := &formatOut{Next: in.Day, Wait: in.For}
		if len(in.Addrs) > 0 {
			out.First = in.Addrs[0]
		}
		if in.Body != nil {
			out.Body = *in.Body
		}
		return out, nil
	}
	return one("/days/{day}", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})})
}

// The document names each type's format, for a parameter, a member, an
// element, a map value, and an output header; the committed copy passes the
// OpenAPI 3.1 meta-schema (uvx openapi-spec-validator
// testdata/formats.openapi.json).
func TestFormatTypesInTheDocument(t *testing.T) {
	var got *formatIn
	app := accepts(t, formatApp(t, &got))
	d := doc(t, app)
	op := at(t, d, "paths", "/days/{day}", "post").(map[string]any)
	params := map[string]any{}
	for _, p := range op["parameters"].([]any) {
		params[p.(map[string]any)["name"].(string)] = p.(map[string]any)["schema"]
	}
	for name, want := range map[string]string{
		// Each states the longest string a request may hold, MaxStringLength
		// (Limits), and an array the most items; a format whose strings have
		// a longest (date, ipv4, ipv6, uuid) states its own, as an enum does.
		"day": `{"format":"date","type":"string"}`, "at": `{"format":"time","maxLength":4096,"type":"string"}`,
		"X-Since": `{"format":"date-time","maxLength":4096,"type":"string"}`, "for": `{"format":"duration","maxLength":4096,"type":"string"}`,
		"addr":     `{"items":{"format":"ipv4","type":"string"},"maxItems":8192,"type":"array"}`,
		"X-Client": `{"format":"ipv4","type":"string"}`, "X-Origin": `{"format":"ipv6","type":"string"}`,
		// A cookie carries no space or semicolon, nor a header whitespace at
		// either end or a control character: the document says so.
		"path":     `{"format":"json-pointer","maxLength":4096,"pattern":"^[\\x20\\x21\\x23-\\x3A\\x3C-\\x5B\\x5D-\\x7E]*$","type":"string"}`,
		"X-Secret": `{"format":"password","maxLength":4096,"pattern":"^(?:[^\\x00-\\x20\\x7F](?:[^\\x00-\\x08\\x0A-\\x1F\\x7F]*[^\\x00-\\x20\\x7F])?)?$","type":"string"}`,
		"pointer":  `{"format":"json-pointer","maxLength":4096,"type":"string"}`, "relative": `{"format":"relative-json-pointer","maxLength":4096,"type":"string"}`,
	} {
		if g := compact(t, params[name]); g != want {
			t.Errorf("parameter %s: %s, want %s", name, g, want)
		}
	}
	// formatBody is read and written: the request's schema, which states the
	// backstops, is formatBody-Input, and the response's states none.
	if g := compact(t, at(t, d, "components", "schemas", "formatBody", "properties", "days")); g != `{"items":{"format":"date","type":"string"},"type":"array","uniqueItems":true}` {
		t.Error(g)
	}
	if g := compact(t, at(t, d, "components", "schemas", "formatBody", "properties", "v6")); g != `{"format":"ipv6","maxLength":20,"type":"string"}` {
		t.Error(g)
	}
	props := at(t, d, "components", "schemas", "formatBody-Input", "properties").(map[string]any)
	for name, want := range map[string]string{
		"date": "date", "time": "time", "duration": "duration", "v4": "ipv4", "v6": "ipv6",
		"pointer": "json-pointer", "relative": "relative-json-pointer", "secret": "password",
		"stamp": "date-time", "instant": "date-time",
	} {
		if g := at(t, props, name, "format"); g != want {
			t.Errorf("member %s: format %v, want %s", name, g, want)
		}
	}
	// A time.Time holds no leap second, and its schema says so; a DateTime
	// holds one.
	if g := compact(t, props["stamp"]); g != `{"format":"date-time","maxLength":4096,"pattern":"^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-5]","type":"string"}` {
		t.Error(g)
	}
	if g := compact(t, props["instant"]); g != `{"format":"date-time","maxLength":4096,"type":"string"}` {
		t.Error(g)
	}
	if g := compact(t, props["v6"]); g != `{"format":"ipv6","maxLength":20,"type":"string"}` {
		t.Error(g)
	}
	if g := compact(t, props["days"]); g != `{"items":{"format":"date","type":"string"},"maxItems":8192,"type":"array","uniqueItems":true}` {
		t.Error(g)
	}
	if g := compact(t, props["byName"]); g != `{"additionalProperties":{"format":"ipv4","type":"string"},"maxProperties":8192,"propertyNames":{"maxLength":4096},"type":"object"}` {
		t.Error(g)
	}
	headers := at(t, op, "responses", "200", "headers").(map[string]any)
	if g := compact(t, at(t, headers, "X-Next", "schema")); g != `{"format":"date","type":"string"}` {
		t.Error(g)
	}
	if g := compact(t, at(t, headers, "X-Wait", "schema")); g != `{"format":"duration","type":"string"}` {
		t.Error(g)
	}
	if g := compact(t, at(t, headers, "X-First", "schema")); g != `{"format":"ipv4","type":"string"}` {
		t.Error(g)
	}
	getatest.Golden(t, app, "testdata/formats.openapi.json")
}

const formatBodyJSON = `{"date":"2024-02-29","time":"23:59:60Z","duration":"P1W",` +
	`"v4":"192.000.002.001","v6":"::ffff:1.2.3.4","pointer":"/a~1b","relative":"1#","secret":"12345678",` +
	`"stamp":"2026-01-02T03:04:05Z","instant":"2016-12-31T23:59:60Z","days":["2024-01-01","2024-01-02"],"byName":{"x":"1.2.3.4"}}`

// A request's values are read into the types and checked against their
// formats, as parameters and in a body, by the one-pass reader and the
// reference path alike.
func TestFormatTypesBindRequests(t *testing.T) {
	var got *formatIn
	app := accepts(t, formatApp(t, &got))
	send := func(path string, header http.Header, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		for k, v := range header {
			req.Header[k] = v
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	q := url.Values{"at": {"08:59:60+09:00"}, "for": {"PT1H"}, "addr": {"010.0.0.1", "192.0.2.1"},
		"pointer": {"/a"}, "relative": {"0#"}}
	h := http.Header{"X-Client": {"010.0.0.1"}, "X-Origin": {"::1"}, "Cookie": {`path=/a~1b`}, "X-Secret": {"pw"},
		"X-Since": {"2016-12-31t15:59:60-08:00"}}
	rec := send("/days/2024-02-29?"+q.Encode(), h, formatBodyJSON)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got.Day.String() != "2024-02-29" || got.At.String() != "08:59:60+09:00" || got.For.String() != "PT1H" ||
		len(got.Addrs) != 2 || got.Addrs[1].String() != "192.0.2.1" || got.Client.String() != "10.0.0.1" ||
		got.Origin.String() != "::1" || got.Path.String() != "/a~1b" ||
		got.Pointer.String() != "/a" || got.Relative.String() != "0#" ||
		string(*got.Secret) != "pw" || got.Body.V4.String() != "192.0.2.1" || got.Body.Maybe != nil ||
		got.Since.String() != "2016-12-31T15:59:60-08:00" || got.Body.Instant.String() != "2016-12-31T23:59:60Z" {
		t.Fatalf("%+v", got)
	}
	// What the handler returns is written as the types write themselves.
	if rec.Header().Get("X-Next") != "2024-02-29" || rec.Header().Get("X-First") != "10.0.0.1" ||
		rec.Header().Get("X-Wait") != "PT1H" {
		t.Fatal(rec.Header())
	}
	var echoed map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatal(err)
	}
	if echoed["v4"] != "192.0.2.1" || echoed["time"] != "23:59:60Z" || echoed["secret"] != "12345678" ||
		echoed["instant"] != "2016-12-31T23:59:60Z" {
		t.Fatal(echoed)
	}

	// Each refused value is named, with its format.
	bad := url.Values{"at": {"24:00:00Z"}, "for": {"P1Y2D"}, "addr": {"1.2.3.4", "1.2.3.256"},
		"pointer": {"a"}, "relative": {"01"}}
	hb := http.Header{"X-Client": {"1.2.3"}, "X-Origin": {"fe80::1%eth0"}, "Cookie": {`path=a`},
		"X-Since": {"2016-12-31T22:59:60Z"}}
	body := strings.NewReplacer(`"2024-02-29"`, `"2023-02-29"`, `"P1W"`, `"P1W1D"`,
		`"2026-01-02T03:04:05Z"`, `"2016-12-31T23:59:60Z"`, `"instant":"2016-12-31T23:59:60Z"`, `"instant":"2016-12-31T23:58:60Z"`,
		`"secret":"12345678"`, `"secret":"short"`, `"/a~1b"`, `"/a~2"`, `["2024-01-01","2024-01-02"]`, `["2024-01-01","2024-01-01"]`,
		`{"x":"1.2.3.4"}`, `{"x":"h_"}`).Replace(formatBodyJSON)
	rec = send("/days/2024-02-30?"+bad.Encode(), hb, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{
		`"path":"day","message":"\"2024-02-30\" is not a valid date"`,
		`"path":"at","message":"\"24:00:00Z\" is not a valid time"`,
		`"path":"for","message":"\"P1Y2D\" is not a valid duration"`,
		`"path":"addr[1]","message":"\"1.2.3.256\" is not a valid ipv4"`,
		`"path":"X-Client","message":"\"1.2.3\" is not a valid ipv4"`,
		`"path":"X-Origin","message":"\"fe80::1%eth0\" is not a valid ipv6"`,
		`"path":"path","message":"\"a\" is not a valid json-pointer"`,
		`"path":"pointer","message":"\"a\" is not a valid json-pointer"`,
		`"path":"relative","message":"\"01\" is not a valid relative-json-pointer"`,
		`"path":"$.date","message":"\"2023-02-29\" is not a valid date"`,
		`"path":"$.duration","message":"\"P1W1D\" is not a valid duration"`,
		`"path":"$.pointer","message":"\"/a~2\" is not a valid json-pointer"`,
		`"path":"$.secret","message":"string length 5 is shorter than minLength 8"`,
		`"path":"$.days","message":"array items at [0] and [1] are equal (uniqueItems)"`,
		`"path":"$.byName.x","message":"\"h_\" is not a valid ipv4"`,
		`"path":"$.stamp","message":"\"2016-12-31T23:59:60Z\" is a leap second, which a time.Time does not hold"`,
		`"path":"$.instant","message":"\"2016-12-31T23:58:60Z\" is not a valid date-time"`,
		`"path":"X-Since","message":"\"2016-12-31T22:59:60Z\" is not a valid date-time"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body)
		}
	}
}

// getaclient sends and reads the format types as geta binds and writes
// them, and getatest checks every response against the document.
func TestTypedClientCarriesFormatTypes(t *testing.T) {
	var got *formatIn
	c := getatest.New(t, formatApp(t, &got)).Typed()
	var body formatBody
	if err := json.Unmarshal([]byte(formatBodyJSON), &body); err != nil {
		t.Fatal(err)
	}
	day, _ := geta.NewDate(2024, 2, 29)
	at, _ := geta.ParseTimeOfDay("23:59:60.5Z")
	wait, _ := geta.DurationOf(90 * time.Second)
	v6, _ := geta.ParseIPv6("2001:db8::1")
	// A cookie value holds no semicolon or space (RFC 6265), so the pointer
	// holds neither.
	path := geta.NewJSONPointer("a/b", "c")
	rel, _ := geta.ParseRelativeJSONPointer("2/a")
	a1, _ := geta.ParseIPv4("192.0.2.1")
	secret := geta.Password("p a s s")
	since, _ := geta.ParseDateTime("2016-12-31T23:59:60.25Z")
	in := &formatIn{Day: day, At: &at, Since: &since, For: &wait, Addrs: []geta.IPv4{a1, a1}, Origin: &v6, Path: &path,
		Relative: &rel, Secret: &secret, Body: &body}
	out, err := getaclient.Call[formatIn, formatOut](t.Context(), c, http.MethodPost, "/days/{day}", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Day != day || *got.At != at || *got.Since != since || *got.For != wait || !slices.Equal(got.Addrs, in.Addrs) || *got.Origin != v6 ||
		!reflect.DeepEqual(*got.Path, path) || *got.Relative != rel ||
		*got.Secret != secret || got.Client != nil || !reflect.DeepEqual(*got.Body, body) {
		t.Fatalf("sent %+v, got %+v", in, got)
	}
	if out.Next != day || out.First != a1 || *out.Wait != wait || !reflect.DeepEqual(out.Body, body) {
		t.Fatalf("received %+v", out)
	}
}

type durationBody struct {
	Wait time.Duration `json:"wait"`
}

type durationParam struct {
	Wait time.Duration `query:"wait"`
}

type durationHeader struct {
	Wait time.Duration `header:"X-Wait"`
	Body ok            `body:"json"`
}

// time.Duration has no JSON form encoding/json/v2 writes or reads, so
// geta.New refuses it wherever it would take one, pointing to
// geta.Duration: it once documented it as an int64 and answered 500 to every
// response holding one.
func TestTimeDurationIsRefused(t *testing.T) {
	const fix = "use geta.Duration"
	rejects(t, one("/x", get(func(context.Context, *empty) (*durationBody, error) { return &durationBody{}, nil })),
		"type time.Duration has no JSON form", fix)
	rejects(t, one("/x", get(func(context.Context, *durationParam) (*ok, error) { return &ok{}, nil })),
		"Wait: type time.Duration has no JSON form", fix)
	rejects(t, one("/x", get(func(context.Context, *empty) (*durationHeader, error) { return &durationHeader{}, nil })),
		"Wait: type time.Duration has no JSON form", fix)
	rejects(t, one("/x", get(func(context.Context, *empty) (*[]time.Duration, error) { return nil, nil })),
		"type time.Duration has no JSON form", fix)
	type named time.Duration // a type of its own, with no JSON quirk
	accepts(t, one("/x", get(func(context.Context, *empty) (*struct {
		N named `json:"n"`
	}, error) {
		return nil, nil
	})))
}

type formatsStamp struct {
	At time.Time `json:"at"`
}

type formatsIn struct {
	At    *time.Time    `query:"at"`
	Str   *string       `query:"str" schema:"format=date-time"`
	Addr  *string       `query:"addr" schema:"format=ipv4"`
	ID    *string       `header:"X-Id" schema:"format=uuid"`
	Delay *string       `cookie:"delay" schema:"format=duration"`
	Body  *formatsStamp `body:"json"`
}

func formatsApp(t *testing.T, got **formatsIn) *geta.App {
	t.Helper()
	h := func(ctx context.Context, in *formatsIn) (*ok, error) {
		*got = in
		return &ok{true}, nil
	}
	return accepts(t, one("/x", geta.Route{Post: geta.Op(http.StatusOK, h, geta.Doc{})}))
}

func formatsSend(app *geta.App, q url.Values, header http.Header, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/x?"+q.Encode(), strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

// A time.Time takes every RFC 3339 date-time but a leap second, which it
// cannot hold, as a parameter and in a body: a lower-case t or z among them.
// It refuses an offset past 23:59 and a comma before the fraction, which
// time.Parse takes. A string of format date-time takes the leap second too.
func TestTimeTakesExactlyRFC3339DateTimes(t *testing.T) {
	var got *formatsIn
	app := formatsApp(t, &got)
	want := time.Date(1963, 6, 19, 8, 30, 6, 283185000, time.UTC)
	rec := formatsSend(app, url.Values{"at": {"1963-06-19t08:30:06.283185z"}, "str": {"1998-12-31T23:59:60Z"}}, nil,
		`{"at":"1963-06-19t08:30:06.283185z"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if !got.At.Equal(want) || !got.Body.At.Equal(want) || *got.Str != "1998-12-31T23:59:60Z" {
		t.Fatalf("%v %v %v", got.At, got.Body.At, *got.Str)
	}
	for _, s := range []string{"1990-12-31T15:59:59-24:00", "1990-12-31T10:00:00+10:60", "2026-07-18T09:30:00,5Z"} {
		rec := formatsSend(app, url.Values{"at": {s}, "str": {s}}, nil, `{"at":"`+s+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", s, rec.Code, rec.Body)
		}
		for _, path := range []string{"at", "str", "$.at"} {
			if want := `"path":"` + path + `","message":"\"` + s + `\" is not a valid date-time"`; !strings.Contains(rec.Body.String(), want) {
				t.Errorf("missing %s in %s", want, rec.Body)
			}
		}
	}
	rec = formatsSend(app, url.Values{"at": {"1998-12-31T23:59:60Z"}}, nil, `{"at":"1998-12-31T15:59:60.5-08:00"}`)
	for _, want := range []string{
		`"path":"at","message":"\"1998-12-31T23:59:60Z\" is a leap second, which a time.Time does not hold"`,
		`"path":"$.at","message":"\"1998-12-31T15:59:60.5-08:00\" is a leap second, which a time.Time does not hold"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body)
		}
	}
}

// A string tagged with a format geta has a type for is held to it, in every
// location; a format geta does not check is refused, as an unknown keyword
// is.
func TestFormatTagsAreChecked(t *testing.T) {
	var got *formatsIn
	app := formatsApp(t, &got)
	const id = "123e4567-e89b-12d3-a456-426614174000"
	rec := formatsSend(app, url.Values{"addr": {"192.0.2.1"}}, http.Header{"X-Id": {id}, "Cookie": {"delay=PT1H"}}, "")
	if rec.Code != http.StatusOK || *got.Addr != "192.0.2.1" || *got.ID != id || *got.Delay != "PT1H" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = formatsSend(app, url.Values{"addr": {"1.2.3.256"}}, http.Header{"X-Id": {"{" + id + "}"}, "Cookie": {"delay=P1Y2D"}}, "")
	for _, want := range []string{
		`"path":"addr","message":"\"1.2.3.256\" is not a valid ipv4"`,
		`"path":"X-Id","message":"\"{` + id + `}\" is not a valid uuid"`,
		`"path":"delay","message":"\"P1Y2D\" is not a valid duration"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %s in %s", want, rec.Body)
		}
	}
	type unchecked struct {
		E string `json:"e" schema:"format=idn-hostname"`
	}
	h := func(context.Context, *empty) (*unchecked, error) { return nil, nil }
	rejects(t, one("/u", get(h)),
		`schema keyword format: unknown format "idn-hostname"; geta checks date, date-time, duration, ipv4`)
	// The formats whose grammar an application draws for itself are an
	// application type's to carry (geta.FormatType), not a schema tag's.
	for _, f := range []string{"regex", "idn-email", "custom", "int64", "binary",
		"email", "hostname", "uri", "uri-reference", "iri", "iri-reference", "uri-template"} {
		if geta.CheckSchemaTag("format="+f, "string") == nil {
			t.Errorf("format %s passed", f)
		}
	}
}
