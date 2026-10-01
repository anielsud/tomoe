package eval

import (
	"strconv"
	"strings"
)

var (
	unitWords = map[string]int{
		"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
		"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
		"seventeen": 17, "eighteen": 18, "nineteen": 19,
	}
	tensWords = map[string]int{
		"twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	}
	scaleWords = map[string]int{"hundred": 100, "thousand": 1000, "million": 1000000, "billion": 1000000000}
	// ordinalWords are converted like their cardinals ("fifteenth" -> "15",
	// matching "15th" -> "15"). "first", "second" and "third" are left
	// alone: they're usually not numbers ("first of all", "a second").
	ordinalWords = map[string]int{
		"fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7, "eighth": 8, "ninth": 9, "tenth": 10,
		"eleventh": 11, "twelfth": 12, "thirteenth": 13, "fourteenth": 14, "fifteenth": 15, "sixteenth": 16,
		"seventeenth": 17, "eighteenth": 18, "nineteenth": 19, "twentieth": 20, "thirtieth": 30,
	}
)

// small parses a number below 100 starting at toks[i] ("seven", "twenty",
// "thirty three", "fifteenth"), returning its value and how many tokens it
// used (0 if toks[i] isn't one).
func small(toks []string, i int) (value, used int) {
	w := toks[i]
	if v, ok := unitWords[w]; ok {
		return v, 1
	}
	if v, ok := ordinalWords[w]; ok {
		return v, 1
	}
	if v, ok := tensWords[w]; ok {
		if i+1 < len(toks) {
			if u, ok := unitWords[toks[i+1]]; ok && u > 0 && u < 10 {
				return v + u, 2
			}
			if u, ok := ordinalWords[toks[i+1]]; ok && u < 10 {
				return v + u, 2
			}
		}
		return v, 1
	}
	return 0, 0
}

// spokenNumbers replaces runs of spoken number words with digits.
func spokenNumbers(toks []string) []string {
	var out []string
	for i := 0; i < len(toks); {
		v, used, ok := number(toks, i)
		if !ok {
			out = append(out, toks[i])
			i++
			continue
		}
		out = append(out, v)
		i += used
	}
	return out
}

// number parses one spoken number starting at toks[i]: an optional "a"
// before a scale ("a hundred"), groups joined by scales and "and" ("two
// hundred and five thousand"), a decimal part ("two point five"), or a year
// said in pairs ("twenty twenty six", "nineteen ninety").
func number(toks []string, i int) (text string, used int, ok bool) {
	j := i
	if toks[j] == "a" {
		if j+1 < len(toks) && scaleWords[toks[j+1]] > 0 {
			j++ // "a hundred": "a" counts as one
		} else {
			return "", 0, false
		}
	}
	total, current, any := 0, 0, false
	if toks[i] == "a" {
		current, any = 1, true
	}
	for j < len(toks) {
		w := toks[j]
		if v, n := small(toks, j); n > 0 {
			if any && current%100 != 0 {
				break // "twenty twenty": a second number starts here
			}
			current += v
			any = true
			j += n
			continue
		}
		if s, ok := scaleWords[w]; ok && any {
			if current == 0 {
				current = 1
			}
			if s == 100 {
				current *= 100
			} else {
				total += current * s
				current = 0
			}
			j++
			continue
		}
		if w == "and" && any && j+1 < len(toks) {
			if _, n := small(toks, j+1); n > 0 && (current%100 == 0 || current >= 100) && current > 0 {
				j++
				continue
			}
		}
		break
	}
	if !any {
		return "", 0, false
	}
	value := total + current

	// A year said in two halves: "twenty twenty six", "nineteen ninety".
	if (value == 19 || value == 20) && total == 0 && j < len(toks) {
		if v, n := small(toks, j); n > 0 && v >= 10 {
			return strconv.Itoa(value*100 + v), j + n - i, true
		}
	}

	// A decimal part: "two point five", "one point two five".
	if j+1 < len(toks) && toks[j] == "point" {
		var digits strings.Builder
		k := j + 1
		for k < len(toks) {
			if d, ok := unitWords[toks[k]]; ok && d < 10 {
				digits.WriteString(strconv.Itoa(d))
				k++
				continue
			}
			break
		}
		if digits.Len() > 0 {
			return strconv.Itoa(value) + "." + digits.String(), k - i, true
		}
	}
	return strconv.Itoa(value), j - i, true
}
