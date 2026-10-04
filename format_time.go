package geta

import (
	"encoding"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Date is a calendar date with no time of day or zone, in format date (an
// RFC 3339 full-date such as 2026-07-18). The zero value is 0000-01-01.
type Date struct {
	year  uint16 // 0 to 9999
	month uint8  // one less than the month
	day   uint8  // one less than the day
}

// NewDate returns the date year-month-day. It returns an error unless the
// date is a day of the proleptic Gregorian calendar in the years 0000 to
// 9999 (RFC 3339 §5.7).
func NewDate(year int, month time.Month, day int) (Date, error) {
	switch {
	case year < 0 || year > 9999:
		return Date{}, fmt.Errorf("geta.NewDate: year %d is outside 0000 to 9999", year)
	case month < time.January || month > time.December:
		return Date{}, fmt.Errorf("geta.NewDate: month %d is outside 1 to 12", month)
	case day < 1 || day > daysIn(month, year):
		return Date{}, fmt.Errorf("geta.NewDate: %s %d has no day %d", month, year, day)
	}
	return Date{year: uint16(year), month: uint8(month - 1), day: uint8(day - 1)}, nil
}

// DateOf returns the date t falls on in its location.
func DateOf(t time.Time) (Date, error) {
	y, m, d := t.Date()
	return NewDate(y, m, d)
}

// ParseDate reads an RFC 3339 full-date.
func ParseDate(s string) (Date, error) {
	d, ok := parseDate(s)
	if !ok {
		return Date{}, formatError("date", []byte(s))
	}
	return d, nil
}

// Year returns the year, 0 to 9999.
func (d Date) Year() int { return int(d.year) }

// Month returns the month.
func (d Date) Month() time.Month { return time.Month(d.month) + 1 }

// Day returns the day of the month, from 1.
func (d Date) Day() int { return int(d.day) + 1 }

// In returns the time the date begins in loc.
func (d Date) In(loc *time.Location) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
}

// String returns the date as RFC 3339 writes it.
func (d Date) String() string {
	b, _ := d.AppendText(nil)
	return string(b)
}

// AppendText appends the date as RFC 3339 writes it.
func (d Date) AppendText(b []byte) ([]byte, error) {
	b = appendDigits(b, d.Year(), 4)
	b = append(b, '-')
	b = appendDigits(b, int(d.Month()), 2)
	b = append(b, '-')
	return appendDigits(b, d.Day(), 2), nil
}

// MarshalText writes the date as RFC 3339 writes it.
func (d Date) MarshalText() ([]byte, error) { return d.AppendText(nil) }

// UnmarshalText reads an RFC 3339 full-date.
func (d *Date) UnmarshalText(b []byte) error {
	v, ok := parseDate(string(b))
	if !ok {
		return formatError("date", b)
	}
	*d = v
	return nil
}

// parseDate reads full-date = date-fullyear "-" date-month "-" date-mday.
func parseDate(s string) (Date, bool) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return Date{}, false
	}
	y, ok1 := digits(s[0:4])
	m, ok2 := digits(s[5:7])
	d, ok3 := digits(s[8:10])
	if !ok1 || !ok2 || !ok3 || m < 1 || m > 12 || d < 1 || d > daysIn(time.Month(m), y) {
		return Date{}, false
	}
	return Date{year: uint16(y), month: uint8(m - 1), day: uint8(d - 1)}, true
}

// daysIn returns the number of days in month of year (RFC 3339 §5.7,
// Appendix C).
func daysIn(month time.Month, year int) int {
	switch month {
	case time.February:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case time.April, time.June, time.September, time.November:
		return 30
	}
	return 31
}

// digits reads a run of ASCII digits short enough for an int.
func digits(s string) (int, bool) {
	n := 0
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, s != ""
}

// appendDigits appends n, which is not negative, in at least width digits.
func appendDigits(b []byte, n, width int) []byte {
	var buf [20]byte
	s := strconv.AppendInt(buf[:0], int64(n), 10)
	for range width - len(s) {
		b = append(b, '0')
	}
	return append(b, s...)
}

// TimeOfDay is a time of day with its offset from UTC, in format time (an
// RFC 3339 full-time such as 09:30:00.5+09:00). Second 60 is a leap second,
// valid only at 23:59 UTC (08:59:60+09:00, for example). A fraction keeps
// every digit written; Nanosecond reads the first nine. The zero value is
// 00:00:00Z.
type TimeOfDay struct {
	hour, minute, second uint8
	frac                 string // the digits of time-secfrac, as written
	sign                 int8   // the offset's sign: 0 for Z, else +1 or -1
	offset               uint16 // the offset's magnitude, in minutes
}

// NewTimeOfDay returns the time hour:minute:second plus nsec nanoseconds, at
// offset seconds east of UTC. offset must be whole minutes below 24 hours; 0
// is written Z. second may be 60 only for a leap second at 23:59 UTC.
func NewTimeOfDay(hour, minute, second, nsec, offset int) (TimeOfDay, error) {
	switch {
	case hour < 0 || hour > 23 || minute < 0 || minute > 59 || second < 0 || second > 60:
		return TimeOfDay{}, fmt.Errorf("geta.NewTimeOfDay: %d:%d:%d is not a time of day", hour, minute, second)
	case nsec < 0 || nsec > 999999999:
		return TimeOfDay{}, fmt.Errorf("geta.NewTimeOfDay: %d nanoseconds is not a fraction of a second", nsec)
	case offset%60 != 0 || offset <= -24*3600 || offset >= 24*3600:
		return TimeOfDay{}, fmt.Errorf("geta.NewTimeOfDay: offset %ds is not whole minutes below 24 hours", offset)
	}
	t := TimeOfDay{hour: uint8(hour), minute: uint8(minute), second: uint8(second)}
	if nsec > 0 {
		t.frac = strings.TrimRight(fmt.Sprintf("%09d", nsec), "0")
	}
	switch {
	case offset > 0:
		t.sign, t.offset = 1, uint16(offset/60)
	case offset < 0:
		t.sign, t.offset = -1, uint16(-offset/60)
	}
	if !t.leapOK() {
		return TimeOfDay{}, fmt.Errorf("geta.NewTimeOfDay: leap second not at 23:59 UTC")
	}
	return t, nil
}

// TimeOfDayOf returns t's time of day and offset.
func TimeOfDayOf(t time.Time) (TimeOfDay, error) {
	h, m, s := t.Clock()
	_, off := t.Zone()
	return NewTimeOfDay(h, m, s, t.Nanosecond(), off)
}

// ParseTimeOfDay reads an RFC 3339 full-time.
func ParseTimeOfDay(s string) (TimeOfDay, error) {
	t, ok := parseTimeOfDay(s)
	if !ok {
		return TimeOfDay{}, formatError("time", []byte(s))
	}
	return t, nil
}

// Hour returns the hour, 0 to 23.
func (t TimeOfDay) Hour() int { return int(t.hour) }

// Minute returns the minute, 0 to 59.
func (t TimeOfDay) Minute() int { return int(t.minute) }

// Second returns the second, 0 to 60.
func (t TimeOfDay) Second() int { return int(t.second) }

// Nanosecond returns the first nine digits of the fraction as nanoseconds.
func (t TimeOfDay) Nanosecond() int {
	n := 0
	for i := range 9 {
		n *= 10
		if i < len(t.frac) {
			n += int(t.frac[i] - '0')
		}
	}
	return n
}

// Offset returns the offset from UTC in seconds east.
func (t TimeOfDay) Offset() int { return int(t.sign) * int(t.offset) * 60 }

// UnknownOffset reports whether the offset is -00:00, which marks a UTC time
// whose local offset is unknown (RFC 3339 §4.3).
func (t TimeOfDay) UnknownOffset() bool { return t.sign < 0 && t.offset == 0 }

// On returns the instant t names on date d, in a fixed zone of its offset
// (UTC for Z and -00:00). A leap second is an error, since a time.Time
// cannot hold one.
func (t TimeOfDay) On(d Date) (time.Time, error) {
	if t.second == 60 {
		return time.Time{}, errors.New("geta.TimeOfDay.On: a time.Time holds no leap second")
	}
	loc := time.UTC
	if off := t.Offset(); off != 0 {
		loc = time.FixedZone("", off)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc), nil
}

// String returns the time as RFC 3339 writes it.
func (t TimeOfDay) String() string {
	b, _ := t.AppendText(nil)
	return string(b)
}

// AppendText appends the time as RFC 3339 writes it.
func (t TimeOfDay) AppendText(b []byte) ([]byte, error) {
	b = appendDigits(b, t.Hour(), 2)
	b = append(b, ':')
	b = appendDigits(b, t.Minute(), 2)
	b = append(b, ':')
	b = appendDigits(b, t.Second(), 2)
	if t.frac != "" {
		b = append(append(b, '.'), t.frac...)
	}
	switch t.sign {
	case 0:
		return append(b, 'Z'), nil
	case 1:
		b = append(b, '+')
	default:
		b = append(b, '-')
	}
	b = appendDigits(b, int(t.offset)/60, 2)
	b = append(b, ':')
	return appendDigits(b, int(t.offset)%60, 2), nil
}

// MarshalText writes the time as RFC 3339 writes it.
func (t TimeOfDay) MarshalText() ([]byte, error) { return t.AppendText(nil) }

// UnmarshalText reads an RFC 3339 full-time.
func (t *TimeOfDay) UnmarshalText(b []byte) error {
	v, ok := parseTimeOfDay(string(b))
	if !ok {
		return formatError("time", b)
	}
	*t = v
	return nil
}

// parseTimeOfDay reads
//
//	full-time    = partial-time time-offset
//	partial-time = time-hour ":" time-minute ":" time-second [time-secfrac]
//	time-secfrac = "." 1*DIGIT
//	time-offset  = "Z" / ("+" / "-") time-hour ":" time-minute
//
// with Z in either case (RFC 3339 §5.6).
func parseTimeOfDay(s string) (TimeOfDay, bool) {
	if len(s) < 9 || s[2] != ':' || s[5] != ':' {
		return TimeOfDay{}, false
	}
	h, ok1 := digits(s[0:2])
	m, ok2 := digits(s[3:5])
	sec, ok3 := digits(s[6:8])
	if !ok1 || !ok2 || !ok3 || h > 23 || m > 59 || sec > 60 {
		return TimeOfDay{}, false
	}
	t := TimeOfDay{hour: uint8(h), minute: uint8(m), second: uint8(sec)}
	s = s[8:]
	if s[0] == '.' {
		i := 1
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == 1 {
			return TimeOfDay{}, false
		}
		t.frac, s = s[1:i], s[i:]
	}
	switch {
	case s == "Z" || s == "z":
	case len(s) == 6 && (s[0] == '+' || s[0] == '-') && s[3] == ':':
		oh, ok1 := digits(s[1:3])
		om, ok2 := digits(s[4:6])
		if !ok1 || !ok2 || oh > 23 || om > 59 {
			return TimeOfDay{}, false
		}
		t.sign, t.offset = 1, uint16(oh*60+om)
		if s[0] == '-' {
			t.sign = -1
		}
	default:
		return TimeOfDay{}, false
	}
	return t, t.leapOK()
}

// parseDateTime reads
//
//	date-time = full-date "T" full-time
//
// with T and Z in either case (RFC 3339 §5.6), and second 60 only at 23:59
// UTC (leapOK).
func parseDateTime(s string) (Date, TimeOfDay, bool) {
	if len(s) < 11 || upper(s[10]) != 'T' {
		return Date{}, TimeOfDay{}, false
	}
	d, ok := parseDate(s[:10])
	if !ok {
		return Date{}, TimeOfDay{}, false
	}
	t, ok := parseTimeOfDay(s[11:])
	return d, t, ok
}

// validDateTime reports whether s is an RFC 3339 date-time.
func validDateTime(s string) bool {
	_, _, ok := parseDateTime(s)
	return ok
}

// DateTime is a date and a time of day with its offset from UTC, in format
// date-time (an RFC 3339 date-time such as 2016-12-31T23:59:60Z). Unlike a
// time.Time, it can hold a leap second. A fraction keeps every digit
// written. The zero value is 0000-01-01T00:00:00Z.
type DateTime struct {
	date Date
	time TimeOfDay
}

// NewDateTime returns the time of day t on the date d.
func NewDateTime(d Date, t TimeOfDay) DateTime { return DateTime{date: d, time: t} }

// DateTimeOf returns t as a date-time. It returns an error unless t's year
// is 0000 to 9999 and its offset is whole minutes below 24 hours.
func DateTimeOf(t time.Time) (DateTime, error) {
	d, err := DateOf(t)
	if err != nil {
		return DateTime{}, err
	}
	tod, err := TimeOfDayOf(t)
	if err != nil {
		return DateTime{}, err
	}
	return DateTime{date: d, time: tod}, nil
}

// ParseDateTime reads an RFC 3339 date-time.
func ParseDateTime(s string) (DateTime, error) {
	d, t, ok := parseDateTime(s)
	if !ok {
		return DateTime{}, formatError("date-time", []byte(s))
	}
	return DateTime{date: d, time: t}, nil
}

// Date returns the date.
func (dt DateTime) Date() Date { return dt.date }

// TimeOfDay returns the time of day and its offset.
func (dt DateTime) TimeOfDay() TimeOfDay { return dt.time }

// LeapSecond reports whether the second is 60.
func (dt DateTime) LeapSecond() bool { return dt.time.second == 60 }

// Time returns the instant as a time.Time, in a fixed zone of its offset
// (UTC for Z and -00:00). A leap second becomes second 0 of the next
// minute, as in POSIX time: 2016-12-31T23:59:60.5Z is
// 2017-01-01T00:00:00.5Z. Use LeapSecond to detect it.
func (dt DateTime) Time() time.Time {
	t := dt.time
	loc := time.UTC
	if off := t.Offset(); off != 0 {
		loc = time.FixedZone("", off)
	}
	d := dt.date
	return time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}

// String returns the date-time as RFC 3339 writes it.
func (dt DateTime) String() string {
	b, _ := dt.AppendText(nil)
	return string(b)
}

// AppendText appends the date-time as RFC 3339 writes it.
func (dt DateTime) AppendText(b []byte) ([]byte, error) {
	b, _ = dt.date.AppendText(b)
	return dt.time.AppendText(append(b, 'T'))
}

// MarshalText writes the date-time as RFC 3339 writes it.
func (dt DateTime) MarshalText() ([]byte, error) { return dt.AppendText(nil) }

// UnmarshalText reads an RFC 3339 date-time.
func (dt *DateTime) UnmarshalText(b []byte) error {
	d, t, ok := parseDateTime(string(b))
	if !ok {
		return formatError("date-time", b)
	}
	*dt = DateTime{date: d, time: t}
	return nil
}

// noLeapSecond is the pattern a time.Time's schema adds to format date-time.
// It requires the tens digit of the second to be 0 to 5, so it matches
// exactly the date-times a time.Time can hold.
var noLeapSecond = impliedPattern{
	pattern: noLeapSecondPattern,
	holds:   regexp.MustCompile(noLeapSecondPattern).MatchString,
	why:     "is a leap second, which a time.Time does not hold",
}

const noLeapSecondPattern = `^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-5]`

// errLeapSecond reports a valid date-time that a time.Time cannot hold.
var errLeapSecond = errors.New("a time.Time holds no leap second")

// readTime reads an RFC 3339 date-time into a time.Time; geta reads every
// request time.Time this way. time.Time.UnmarshalText's grammar differs from
// RFC 3339's, so the text is checked by parseDateTime first and T and Z are
// upper-cased before it is handed over. A leap second is errLeapSecond.
func readTime(s string) (time.Time, error) {
	_, tod, ok := parseDateTime(s)
	if !ok {
		return time.Time{}, formatError("date-time", []byte(s))
	}
	if tod.second == 60 {
		return time.Time{}, fmt.Errorf("%q: %w", s, errLeapSecond)
	}
	b := []byte(s)
	b[10] = 'T'
	if last := len(b) - 1; b[last] == 'z' {
		b[last] = 'Z'
	}
	var t time.Time
	err := t.UnmarshalText(b)
	return t, err
}

// unmarshalText reads request text into p, a pointer to a text type, using
// readTime for a time.Time and UnmarshalText otherwise.
func unmarshalText(p any, text []byte) error {
	if t, ok := p.(*time.Time); ok {
		var err error
		*t, err = readTime(string(text))
		return err
	}
	return p.(encoding.TextUnmarshaler).UnmarshalText(text)
}

// leapOK reports whether a second of 60 falls at 23:59 UTC after applying
// the offset (RFC 3339 §5.7). The date is not checked, since leap-second
// days are announced by the IERS.
func (t TimeOfDay) leapOK() bool {
	if t.second != 60 {
		return true
	}
	utc := (int(t.hour)*60 + int(t.minute) - int(t.sign)*int(t.offset)) % (24 * 60)
	if utc < 0 {
		utc += 24 * 60
	}
	return utc == 23*60+59
}

// Duration is an amount of time in calendar and clock units, in format
// duration (an RFC 3339 Appendix A duration such as P1Y2M3DT4H5M6S or P2W).
// Components are whole numbers in grammar order with no unit skipped, so
// P1Y2D is invalid. It keeps the text it was read from. The zero value is
// PT0S.
type Duration struct {
	text string // a valid duration, or "" for PT0S
}

// DurationParts holds a duration's components. An omitted component is zero.
type DurationParts struct {
	Years, Months, Weeks, Days, Hours, Minutes, Seconds uint64
}

// ParseDuration reads an RFC 3339 Appendix A duration.
func ParseDuration(s string) (Duration, error) {
	if !validDuration(s) {
		return Duration{}, formatError("duration", []byte(s))
	}
	return Duration{text: s}, nil
}

// DurationOf returns d in hours, minutes, and seconds. It returns an error
// if d is negative or not a whole number of seconds.
func DurationOf(d time.Duration) (Duration, error) {
	if d < 0 || d%time.Second != 0 {
		return Duration{}, fmt.Errorf("geta.DurationOf: %s is not a whole, non-negative number of seconds", d)
	}
	s := int64(d / time.Second)
	h, m := s/3600, s/60%60
	s %= 60
	// Units may not skip: hours and seconds need minutes between them.
	b := []byte("PT")
	unit := func(n int64, u byte) { b = append(strconv.AppendInt(b, n, 10), u) }
	switch {
	case h > 0:
		unit(h, 'H')
		if m > 0 || s > 0 {
			unit(m, 'M')
		}
		if s > 0 {
			unit(s, 'S')
		}
	case m > 0:
		unit(m, 'M')
		if s > 0 {
			unit(s, 'S')
		}
	default:
		unit(s, 'S')
	}
	return Duration{text: string(b)}, nil
}

// Parts returns the duration's components. It returns an error if a
// component exceeds a uint64.
func (d Duration) Parts() (DurationParts, error) {
	var p DurationParts
	s := d.String()
	inTime := false
	for i := 1; i < len(s); {
		if upper(s[i]) == 'T' {
			inTime = true
			i++
			continue
		}
		j := i
		for isDigit(s[i]) {
			i++
		}
		n, err := strconv.ParseUint(s[j:i], 10, 64)
		if err != nil {
			return DurationParts{}, fmt.Errorf("geta.Duration: component %s of %s does not fit a uint64", s[j:i], s)
		}
		switch c := upper(s[i]); {
		case c == 'Y':
			p.Years = n
		case c == 'M' && !inTime:
			p.Months = n
		case c == 'W':
			p.Weeks = n
		case c == 'D':
			p.Days = n
		case c == 'H':
			p.Hours = n
		case c == 'M':
			p.Minutes = n
		case c == 'S':
			p.Seconds = n
		}
		i++
	}
	return p, nil
}

// Std returns the duration as a time.Duration. It returns an error if a
// year, month, week, or day component is non-zero, since those have no
// fixed length, or if the result exceeds a time.Duration. Use AddTo for
// calendar durations.
func (d Duration) Std() (time.Duration, error) {
	p, err := d.Parts()
	if err != nil {
		return 0, err
	}
	if p.Years != 0 || p.Months != 0 || p.Weeks != 0 || p.Days != 0 {
		return 0, fmt.Errorf("geta.Duration.Std: %s has calendar components; use AddTo", d)
	}
	return clockDuration(d, p)
}

// clockDuration returns p's hours, minutes, and seconds as a time.Duration.
func clockDuration(d Duration, p DurationParts) (time.Duration, error) {
	const max = uint64(math.MaxInt64 / int64(time.Second))
	sum, ok := uint64(0), true
	for _, c := range []struct{ n, unit uint64 }{{p.Hours, 3600}, {p.Minutes, 60}, {p.Seconds, 1}} {
		if c.n > max/c.unit || sum > max-c.n*c.unit {
			ok = false
			break
		}
		sum += c.n * c.unit
	}
	if !ok {
		return 0, fmt.Errorf("geta.Duration: %s exceeds a time.Duration", d)
	}
	return time.Duration(sum) * time.Second, nil
}

// AddTo returns t plus the duration: years, months, weeks, and days by
// time.Time.AddDate, then hours, minutes, and seconds as elapsed time. It
// returns an error if a component is too large for that arithmetic.
func (d Duration) AddTo(t time.Time) (time.Time, error) {
	p, err := d.Parts()
	if err != nil {
		return time.Time{}, err
	}
	const max = math.MaxInt32
	if p.Years > max || p.Months > max || p.Weeks > max/7 || p.Days > max-7*p.Weeks {
		return time.Time{}, fmt.Errorf("geta.Duration.AddTo: %s exceeds the calendar arithmetic of time.Time", d)
	}
	clock, err := clockDuration(d, p)
	if err != nil {
		return time.Time{}, err
	}
	return t.AddDate(int(p.Years), int(p.Months), int(7*p.Weeks+p.Days)).Add(clock), nil
}

// String returns the duration's text.
func (d Duration) String() string {
	if d.text == "" {
		return "PT0S"
	}
	return d.text
}

// AppendText appends the duration's text.
func (d Duration) AppendText(b []byte) ([]byte, error) { return append(b, d.String()...), nil }

// MarshalText writes the duration's text.
func (d Duration) MarshalText() ([]byte, error) { return d.AppendText(nil) }

// UnmarshalText reads an RFC 3339 Appendix A duration.
func (d *Duration) UnmarshalText(b []byte) error {
	s := string(b)
	if !validDuration(s) {
		return formatError("duration", b)
	}
	d.text = s
	return nil
}

// validDuration reports whether s is
//
//	duration   = "P" (dur-date / dur-time / dur-week)
//	dur-date   = (dur-day / dur-month / dur-year) [dur-time]
//	dur-time   = "T" (dur-hour / dur-minute / dur-second)
//	dur-year   = 1*DIGIT "Y" [dur-month]
//	dur-month  = 1*DIGIT "M" [dur-day]
//	dur-day    = 1*DIGIT "D"
//	dur-hour   = 1*DIGIT "H" [dur-minute]
//	dur-minute = 1*DIGIT "M" [dur-second]
//	dur-second = 1*DIGIT "S"
//	dur-week   = 1*DIGIT "W"
//
// with letters in either case.
func validDuration(s string) bool {
	if len(s) < 3 || upper(s[0]) != 'P' {
		return false
	}
	i, n, week, ok := durationRun(s, 1, "YMD")
	if !ok {
		return false
	}
	if week {
		return i == len(s)
	}
	if i == len(s) {
		return n > 0
	}
	if upper(s[i]) != 'T' {
		return false
	}
	i, n, week, ok = durationRun(s, i+1, "HMS")
	return ok && !week && n > 0 && i == len(s)
}

// durationRun reads components from s[i:] whose units are consecutive
// letters of order, and returns where it stopped and how many it read. week
// reports a sole week component in the date position.
func durationRun(s string, i int, order string) (end, n int, week, ok bool) {
	last := -1
	for i < len(s) && isDigit(s[i]) {
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == len(s) {
			return i, n, false, false // a number with no unit
		}
		u := upper(s[i])
		i++
		if u == 'W' && order == "YMD" && n == 0 {
			return i, 1, true, true
		}
		k := strings.IndexByte(order, u)
		if k < 0 || last >= 0 && k != last+1 {
			return i, n, false, false
		}
		last = k
		n++
	}
	return i, n, false, true
}
