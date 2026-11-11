package vbs

import (
	"strings"
	"time"
)

// dateArg resolves a date argument; null reports a Null argument.
func dateArg(v Value) (t time.Time, null bool, err error) {
	if v, err = scalar(v); err != nil {
		return time.Time{}, false, err
	}
	if isNull(v) {
		return time.Time{}, true, nil
	}
	t, err = toDate(v)
	return t, false, err
}

// datePartFn registers a function extracting one field of a date.
func datePartFn(name string, fn func(time.Time) int) {
	reg(name, 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		t, null, err := dateArg(args[0])
		if err != nil || null {
			return Null, err
		}
		return int64(fn(t)), nil
	})
}

// firstDayArg reads an optional first-day-of-week argument and returns it
// as a time.Weekday (0 = Sunday). vbUseSystem (0) means Sunday.
func firstDayArg(args []Value, i int) (time.Weekday, error) {
	n, err := optInt(args, i, 1)
	if err != nil {
		return 0, err
	}
	if n < 0 || n > 7 {
		return 0, newErr(5, "")
	}
	if n == 0 {
		n = 1
	}
	return time.Weekday(n - 1), nil
}

// weekdayNumber returns the 1-based day of the week of t counted from
// first.
func weekdayNumber(t time.Time, first time.Weekday) int {
	return (int(t.Weekday())-int(first)+7)%7 + 1
}

// addMonths adds n months, clamping the day to the end of the month.
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	total := int(m) - 1 + n
	y += total / 12
	mo := total % 12
	if mo < 0 {
		mo += 12
		y--
	}
	mo++
	if dim := daysIn(y, mo); d > dim {
		d = dim
	}
	h, mi, s := t.Clock()
	return makeDate(y, mo, d, h, mi, s)
}

func checkDateRange(t time.Time) (Value, error) {
	if y := t.Year(); y < 100 || y > 9999 {
		return nil, newErr(5, "")
	}
	return t, nil
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// weekOfYear implements DatePart("ww", ...).
func weekOfYear(t time.Time, first time.Weekday, mode int64) int {
	y := t.Year()
	jan1 := makeDate(y, 1, 1, 0, 0, 0)
	offset := weekdayNumber(jan1, first) - 1 // days of week 1 before Jan 1
	week := (t.YearDay()-1+offset)/7 + 1
	daysInFirst := 7 - offset
	switch mode {
	case 2: // first week with at least four days
		if daysInFirst < 4 {
			week--
		}
	case 3: // first full week
		if daysInFirst < 7 {
			week--
		}
	default:
		return week
	}
	if week == 0 {
		// Belongs to the last week of the previous year.
		return weekOfYear(makeDate(y-1, 12, 31, 0, 0, 0), first, mode)
	}
	if mode == 2 {
		// The last days of December may belong to week 1 of next year.
		next := makeDate(y+1, 1, 1, 0, 0, 0)
		off := weekdayNumber(next, first) - 1
		if 7-off >= 4 && dayNumber(next)-dayNumber(t) <= int64(off) {
			return 1
		}
	}
	return week
}

func init() {
	reg("Now", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		return nowDate(), nil
	})
	reg("Date", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		n := nowDate()
		return makeDate(n.Year(), int(n.Month()), n.Day(), 0, 0, 0), nil
	})
	reg("Time", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		n := nowDate()
		return makeDate(1899, 12, 30, n.Hour(), n.Minute(), n.Second()), nil
	})
	reg("Timer", 0, 0, func(in *Interpreter, args []Value) (Value, error) {
		n := time.Now()
		secs := float64(n.Hour()*3600+n.Minute()*60+n.Second()) + float64(n.Nanosecond()/1e7)/100
		return secs, nil
	})

	datePartFn("Year", func(t time.Time) int { return t.Year() })
	datePartFn("Month", func(t time.Time) int { return int(t.Month()) })
	datePartFn("Day", func(t time.Time) int { return t.Day() })
	datePartFn("Hour", func(t time.Time) int { return t.Hour() })
	datePartFn("Minute", func(t time.Time) int { return t.Minute() })
	datePartFn("Second", func(t time.Time) int { return t.Second() })

	reg("Weekday", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		t, null, err := dateArg(args[0])
		if err != nil || null {
			return Null, err
		}
		first, err := firstDayArg(args, 1)
		if err != nil {
			return nil, err
		}
		return int64(weekdayNumber(t, first)), nil
	})
	reg("WeekdayName", 1, 3, func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < 1 || n > 7 {
			return nil, newErr(5, "")
		}
		abbrev := false
		if v, ok := optArg(args, 1); ok {
			if abbrev, err = toBool(v); err != nil {
				return nil, err
			}
		}
		first, err := firstDayArg(args, 2)
		if err != nil {
			return nil, err
		}
		name := weekdayNames[(int(n)-1+int(first))%7]
		if abbrev {
			name = name[:3]
		}
		return name, nil
	})
	reg("MonthName", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		n, err := toInt(args[0])
		if err != nil {
			return nil, err
		}
		if n < 1 || n > 12 {
			return nil, newErr(5, "")
		}
		name := monthNames[n-1]
		if v, ok := optArg(args, 1); ok {
			abbrev, err := toBool(v)
			if err != nil {
				return nil, err
			}
			if abbrev {
				name = name[:3]
			}
		}
		return name, nil
	})

	reg("DateSerial", 3, 3, func(in *Interpreter, args []Value) (Value, error) {
		var n [3]int64
		for i := range n {
			v, err := toInt(args[i])
			if err != nil {
				return nil, err
			}
			if v < -100000 || v > 100000 {
				return nil, newErr(5, "")
			}
			n[i] = v
		}
		y := int(n[0])
		switch {
		case y >= 0 && y <= 29:
			y += 2000
		case y >= 30 && y <= 99:
			y += 1900
		}
		return checkDateRange(makeDate(y, int(n[1]), int(n[2]), 0, 0, 0))
	})
	reg("TimeSerial", 3, 3, func(in *Interpreter, args []Value) (Value, error) {
		var n [3]int64
		for i := range n {
			v, err := toInt(args[i])
			if err != nil {
				return nil, err
			}
			if v < -1000000 || v > 1000000 {
				return nil, newErr(5, "")
			}
			n[i] = v
		}
		return checkDateRange(makeDate(1899, 12, 30, int(n[0]), int(n[1]), int(n[2])))
	})
	reg("DateValue", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		t, null, err := dateArg(args[0])
		if err != nil || null {
			return Null, err
		}
		return makeDate(t.Year(), int(t.Month()), t.Day(), 0, 0, 0), nil
	})
	reg("TimeValue", 1, 1, func(in *Interpreter, args []Value) (Value, error) {
		t, null, err := dateArg(args[0])
		if err != nil || null {
			return Null, err
		}
		return makeDate(1899, 12, 30, t.Hour(), t.Minute(), t.Second()), nil
	})

	reg("DateAdd", 3, 3, func(in *Interpreter, args []Value) (Value, error) {
		interval, err := toStr(args[0])
		if err != nil {
			return nil, err
		}
		nf, err := toFloat(args[1])
		if err != nil {
			return nil, err
		}
		t, null, err := dateArg(args[2])
		if err != nil || null {
			return Null, err
		}
		if nf > 1e9 || nf < -1e9 {
			return nil, newErr(5, "")
		}
		n := int(nf) // the fraction is ignored
		y, mo, d := t.Date()
		h, mi, s := t.Clock()
		var r time.Time
		switch strings.ToLower(strings.TrimSpace(interval)) {
		case "yyyy":
			r = addMonths(t, 12*n)
		case "q":
			r = addMonths(t, 3*n)
		case "m":
			r = addMonths(t, n)
		case "y", "d", "w":
			r = makeDate(y, int(mo), d+n, h, mi, s)
		case "ww":
			r = makeDate(y, int(mo), d+7*n, h, mi, s)
		case "h":
			r = makeDate(y, int(mo), d, h+n, mi, s)
		case "n":
			r = makeDate(y, int(mo), d, h, mi+n, s)
		case "s":
			r = makeDate(y, int(mo), d, h, mi, s+n)
		default:
			return nil, newErr(5, "")
		}
		return checkDateRange(r)
	})
	reg("DateDiff", 3, 5, func(in *Interpreter, args []Value) (Value, error) {
		interval, err := toStr(args[0])
		if err != nil {
			return nil, err
		}
		t1, null1, err := dateArg(args[1])
		if err != nil {
			return nil, err
		}
		t2, null2, err := dateArg(args[2])
		if err != nil {
			return nil, err
		}
		first, err := firstDayArg(args, 3)
		if err != nil {
			return nil, err
		}
		if null1 || null2 {
			return Null, nil
		}
		d1, d2 := dayNumber(t1), dayNumber(t2)
		s1, s2 := d1*86400+secondOfDay(t1), d2*86400+secondOfDay(t2)
		switch strings.ToLower(strings.TrimSpace(interval)) {
		case "yyyy":
			return int64(t2.Year() - t1.Year()), nil
		case "q":
			return int64((t2.Year()-t1.Year())*4 + (int(t2.Month())-1)/3 - (int(t1.Month())-1)/3), nil
		case "m":
			return int64((t2.Year()-t1.Year())*12 + int(t2.Month()) - int(t1.Month())), nil
		case "y", "d":
			return d2 - d1, nil
		case "w":
			return (d2 - d1) / 7, nil
		case "ww":
			w1 := d1 - int64(weekdayNumber(t1, first)-1)
			w2 := d2 - int64(weekdayNumber(t2, first)-1)
			return (w2 - w1) / 7, nil
		case "h":
			return floorDiv(s2, 3600) - floorDiv(s1, 3600), nil
		case "n":
			return floorDiv(s2, 60) - floorDiv(s1, 60), nil
		case "s":
			return s2 - s1, nil
		}
		return nil, newErr(5, "")
	})
	reg("DatePart", 2, 4, func(in *Interpreter, args []Value) (Value, error) {
		interval, err := toStr(args[0])
		if err != nil {
			return nil, err
		}
		t, null, err := dateArg(args[1])
		if err != nil {
			return nil, err
		}
		first, err := firstDayArg(args, 2)
		if err != nil {
			return nil, err
		}
		mode, err := optInt(args, 3, 1)
		if err != nil {
			return nil, err
		}
		if null {
			return Null, nil
		}
		switch strings.ToLower(strings.TrimSpace(interval)) {
		case "yyyy":
			return int64(t.Year()), nil
		case "q":
			return int64((int(t.Month())-1)/3 + 1), nil
		case "m":
			return int64(t.Month()), nil
		case "y":
			return int64(t.YearDay()), nil
		case "d":
			return int64(t.Day()), nil
		case "w":
			return int64(weekdayNumber(t, first)), nil
		case "ww":
			return int64(weekOfYear(t, first, mode)), nil
		case "h":
			return int64(t.Hour()), nil
		case "n":
			return int64(t.Minute()), nil
		case "s":
			return int64(t.Second()), nil
		}
		return nil, newErr(5, "")
	})
	reg("FormatDateTime", 1, 2, func(in *Interpreter, args []Value) (Value, error) {
		t, null, err := dateArg(args[0])
		if err != nil {
			return nil, err
		}
		if null {
			return nil, newErr(94, "")
		}
		mode, err := optInt(args, 1, 0)
		if err != nil {
			return nil, err
		}
		switch mode {
		case 0:
			return formatDateGeneral(t), nil
		case 1:
			return formatLongDate(t), nil
		case 2:
			return formatShortDate(t), nil
		case 3:
			return formatLongTime(t), nil
		case 4:
			return formatShortTime(t), nil
		}
		return nil, newErr(5, "")
	})
}
