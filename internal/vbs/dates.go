package vbs

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Dates
//
// A VBScript Date has no time zone: it is a wall-clock reading stored as a
// Double counting days since 30 December 1899. The interpreter keeps dates as
// time.Time values and only ever looks at their wall-clock fields; values it
// produces itself (Now, DateSerial, CDate, ...) are located in time.UTC with
// the local wall-clock reading. Hosts receiving a Date should therefore read
// the fields (Year, Hour, ...) rather than the instant.

var oleEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

var monthNames = [...]string{"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December"}

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func makeDate(y, mo, d, h, mi, s int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC)
}

// nowDate returns the current local wall-clock time as a script date.
func nowDate() time.Time {
	n := time.Now()
	return makeDate(n.Year(), int(n.Month()), n.Day(), n.Hour(), n.Minute(), n.Second())
}

// dayNumber returns the number of days between the OLE epoch and the
// calendar day of t.
func dayNumber(t time.Time) int64 {
	y, mo, d := t.Date()
	day := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
	return (day.Unix() - oleEpoch.Unix()) / 86400
}

// secondOfDay returns the number of seconds since midnight of t.
func secondOfDay(t time.Time) int64 {
	h, mi, s := t.Clock()
	return int64(h*3600 + mi*60 + s)
}

// dateToFloat converts the wall-clock reading of t to an OLE automation date.
func dateToFloat(t time.Time) float64 {
	days := float64(dayNumber(t))
	frac := (float64(secondOfDay(t)) + float64(t.Nanosecond())/1e9) / 86400
	if days < 0 {
		return days - frac
	}
	return days + frac
}

// floatToDate converts an OLE automation date to a script date (rounded to
// the nearest second).
func floatToDate(f float64) (time.Time, error) {
	if math.IsNaN(f) || f <= -657435 || f >= 2958466 {
		return time.Time{}, newErr(6, "")
	}
	ip := math.Trunc(f)
	frac := math.Abs(f - ip)
	secs := int(math.Round(frac * 86400))
	return time.Date(1899, 12, 30+int(ip), 0, 0, secs, 0, time.UTC), nil
}

func daysIn(y, m int) int {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func formatShortDate(t time.Time) string {
	return fmt.Sprintf("%d/%d/%d", int(t.Month()), t.Day(), t.Year())
}

func formatLongDate(t time.Time) string {
	return fmt.Sprintf("%s, %s %d, %d", t.Weekday().String(), t.Month().String(), t.Day(), t.Year())
}

func formatLongTime(t time.Time) string {
	h, mi, s := t.Clock()
	ap := "AM"
	if h >= 12 {
		ap = "PM"
	}
	h12 := h % 12
	if h12 == 0 {
		h12 = 12
	}
	return fmt.Sprintf("%d:%02d:%02d %s", h12, mi, s, ap)
}

func formatShortTime(t time.Time) string {
	h, mi, _ := t.Clock()
	return fmt.Sprintf("%02d:%02d", h, mi)
}

// formatDateGeneral renders a date the way CStr does (US English locale):
// the date part is omitted for pure times and the time part for midnight.
func formatDateGeneral(t time.Time) string {
	hasDate := dayNumber(t) != 0
	hasTime := secondOfDay(t) != 0
	switch {
	case hasDate && hasTime:
		return formatShortDate(t) + " " + formatLongTime(t)
	case hasDate:
		return formatShortDate(t)
	}
	return formatLongTime(t)
}

type dateTok struct {
	kind byte // 'n' number, 'w' word, or the separator character
	num  int
	ndig int
	word string
}

func monthFromName(w string) int {
	if len(w) < 3 {
		return 0
	}
	for i, n := range monthNames {
		ln := strings.ToLower(n)
		if w == ln || w == ln[:3] || (i == 8 && w == "sept") {
			return i + 1
		}
	}
	return 0
}

func isWeekdayName(w string) bool {
	if len(w) < 3 {
		return false
	}
	for _, n := range weekdayNames {
		ln := strings.ToLower(n)
		if w == ln || w == ln[:3] {
			return true
		}
	}
	return w == "tues" || w == "thur" || w == "thurs"
}

func expandYear(t dateTok) int {
	if t.ndig <= 2 {
		if t.num < 30 {
			return 2000 + t.num
		}
		return 1900 + t.num
	}
	return t.num
}

// parseDate parses the date/time notations VBScript scripts commonly use
// with the US English locale: m/d/yyyy, yyyy-mm-dd, d.m.yyyy, month names,
// optional time with or without AM/PM, and time-only strings.
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	var toks []dateTok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case isDigit(c):
			j, n := i, 0
			for j < len(s) && isDigit(s[j]) {
				n = n*10 + int(s[j]-'0')
				j++
				if j-i > 9 {
					return time.Time{}, false
				}
			}
			toks = append(toks, dateTok{kind: 'n', num: n, ndig: j - i})
			i = j
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			j := i
			for j < len(s) && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z')) {
				j++
			}
			toks = append(toks, dateTok{kind: 'w', word: strings.ToLower(s[i:j])})
			i = j
		case c == '/' || c == '-' || c == '.' || c == ',' || c == ':':
			toks = append(toks, dateTok{kind: c})
			i++
		default:
			return time.Time{}, false
		}
	}

	var nums []dateTok
	month := 0
	hh, mm, ss := 0, 0, 0
	hasTime := false
	ampm := 0 // 1 AM, 2 PM
	slashes, dashes, dots := 0, 0, 0
	for k := 0; k < len(toks); {
		t := toks[k]
		switch t.kind {
		case 'n':
			if k+1 < len(toks) && toks[k+1].kind == ':' {
				if hasTime {
					return time.Time{}, false
				}
				hh = t.num
				k += 2
				if k >= len(toks) || toks[k].kind != 'n' {
					return time.Time{}, false
				}
				mm = toks[k].num
				k++
				if k < len(toks) && toks[k].kind == ':' {
					k++
					if k >= len(toks) || toks[k].kind != 'n' {
						return time.Time{}, false
					}
					ss = toks[k].num
					k++
					if k+1 < len(toks) && toks[k].kind == '.' && toks[k+1].kind == 'n' {
						k += 2 // fractional seconds are dropped
					}
				}
				hasTime = true
				continue
			}
			nums = append(nums, t)
		case 'w':
			w := t.word
			switch {
			case w == "am" || w == "a" || w == "pm" || w == "p":
				if ampm != 0 {
					return time.Time{}, false
				}
				ampm = 1
				if w[0] == 'p' {
					ampm = 2
				}
				if !hasTime {
					if len(nums) == 0 {
						return time.Time{}, false
					}
					hh = nums[len(nums)-1].num
					nums = nums[:len(nums)-1]
					hasTime = true
				}
			case monthFromName(w) > 0:
				if month != 0 {
					return time.Time{}, false
				}
				month = monthFromName(w)
			case isWeekdayName(w):
			case (w == "t" || w == "z") && k > 0:
			default:
				return time.Time{}, false
			}
		case '/':
			slashes++
		case '-':
			if k == 0 {
				return time.Time{}, false
			}
			dashes++
		case '.':
			dots++
		case ',':
		default:
			return time.Time{}, false
		}
		k++
	}

	y, m, d := 1899, 12, 30
	thisYear := time.Now().Year()
	switch {
	case month != 0:
		m = month
		switch len(nums) {
		case 1:
			if nums[0].ndig > 2 || nums[0].num > 31 {
				y, d = expandYear(nums[0]), 1
			} else {
				y, d = thisYear, nums[0].num
			}
		case 2:
			if nums[0].ndig > 2 || nums[0].num > 31 {
				y, d = expandYear(nums[0]), nums[1].num
			} else {
				d, y = nums[0].num, expandYear(nums[1])
			}
		default:
			return time.Time{}, false
		}
	case len(nums) == 3:
		if slashes+dashes+dots == 0 {
			return time.Time{}, false
		}
		a, b, c := nums[0], nums[1], nums[2]
		switch {
		case a.ndig > 2:
			y, m, d = a.num, b.num, c.num
		case dots > 0 && slashes == 0 && dashes == 0:
			d, m, y = a.num, b.num, expandYear(c)
		default:
			m, d, y = a.num, b.num, expandYear(c)
			if m > 12 && d <= 12 {
				m, d = d, m
			}
		}
	case len(nums) == 2:
		if slashes+dashes == 0 {
			return time.Time{}, false
		}
		a, b := nums[0], nums[1]
		switch {
		case a.ndig > 2:
			y, m, d = a.num, b.num, 1
		case b.ndig > 2:
			m, y, d = a.num, b.num, 1
		default:
			m, d, y = a.num, b.num, thisYear
			if m > 12 && d <= 12 {
				m, d = d, m
			}
		}
	case len(nums) == 0:
		if !hasTime {
			return time.Time{}, false
		}
	default:
		return time.Time{}, false
	}
	if y < 100 || y > 9999 || m < 1 || m > 12 || d < 1 || d > daysIn(y, m) {
		return time.Time{}, false
	}
	if hasTime {
		if ampm != 0 {
			if hh < 1 || hh > 12 {
				return time.Time{}, false
			}
			if ampm == 2 && hh < 12 {
				hh += 12
			}
			if ampm == 1 && hh == 12 {
				hh = 0
			}
		}
		if hh > 23 || mm > 59 || ss > 59 {
			return time.Time{}, false
		}
	}
	return makeDate(y, m, d, hh, mm, ss), true
}
