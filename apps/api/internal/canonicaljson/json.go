package canonicaljson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
)

// Value marshals a Go value and returns its canonical JSON representation.
func Value(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return Bytes(raw)
}

// Bytes parses one JSON value and returns its canonical representation.
func Bytes(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}

	var result bytes.Buffer
	if err := write(&result, document); err != nil {
		return nil, err
	}
	return result.Bytes(), nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func write(target *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		target.WriteString("null")
	case bool:
		if typed {
			target.WriteString("true")
		} else {
			target.WriteString("false")
		}
	case string:
		writeString(target, typed)
	case json.Number:
		formatted, err := number(typed)
		if err != nil {
			return err
		}
		target.WriteString(formatted)
	case []any:
		target.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				target.WriteByte(',')
			}
			if err := write(target, item); err != nil {
				return err
			}
		}
		target.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(first, second int) bool {
			return lessUTF16(keys[first], keys[second])
		})
		target.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				target.WriteByte(',')
			}
			writeString(target, key)
			target.WriteByte(':')
			if err := write(target, typed[key]); err != nil {
				return err
			}
		}
		target.WriteByte('}')
	default:
		return fmt.Errorf("unsupported canonical JSON value %T", value)
	}
	return nil
}

func number(number json.Number) (string, error) {
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return "", fmt.Errorf("invalid JSON number %q", number)
	}
	if value == 0 {
		return "0", nil
	}

	format := byte('f')
	absolute := math.Abs(value)
	if absolute < 1e-6 || absolute >= 1e21 {
		format = 'e'
	}
	formatted := strconv.FormatFloat(value, format, -1, 64)
	if format == 'e' {
		formatted = trimExponentZero(formatted)
	}
	return formatted, nil
}

func trimExponentZero(value string) string {
	for index := 0; index+2 < len(value); index++ {
		if value[index] != 'e' || (value[index+1] != '+' && value[index+1] != '-') {
			continue
		}
		for index+2 < len(value)-1 && value[index+2] == '0' {
			value = value[:index+2] + value[index+3:]
		}
		break
	}
	return value
}

func writeString(target *bytes.Buffer, value string) {
	const hexadecimal = "0123456789abcdef"
	target.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			target.WriteByte('\\')
			target.WriteRune(character)
		case '\b':
			target.WriteString(`\b`)
		case '\f':
			target.WriteString(`\f`)
		case '\n':
			target.WriteString(`\n`)
		case '\r':
			target.WriteString(`\r`)
		case '\t':
			target.WriteString(`\t`)
		default:
			if character < 0x20 {
				target.WriteString(`\u00`)
				target.WriteByte(hexadecimal[byte(character)>>4])
				target.WriteByte(hexadecimal[byte(character)&0x0f])
			} else {
				target.WriteRune(character)
			}
		}
	}
	target.WriteByte('"')
}

func lessUTF16(first, second string) bool {
	firstUnits := utf16.Encode([]rune(first))
	secondUnits := utf16.Encode([]rune(second))
	limit := len(firstUnits)
	if len(secondUnits) < limit {
		limit = len(secondUnits)
	}
	for index := 0; index < limit; index++ {
		if firstUnits[index] != secondUnits[index] {
			return firstUnits[index] < secondUnits[index]
		}
	}
	return len(firstUnits) < len(secondUnits)
}
