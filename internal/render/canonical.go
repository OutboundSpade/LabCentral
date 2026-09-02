package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func DecodeJSON(input []byte) (any, error) {
	if !utf8.Valid(input) {
		return nil, renderError(StageCanonicalization, "invalid_utf8", "", "JSON is not valid UTF-8", nil)
	}
	if err := validateJSONSurrogates(input); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, "")
	if err != nil {
		if typed, ok := err.(*Error); ok {
			return nil, typed
		}
		return nil, renderError(StageCanonicalization, "invalid_json", "", "JSON syntax is invalid", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, renderError(StageCanonicalization, "multiple_json_values", "", "exactly one JSON value is required", err)
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder, currentPath string) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		result := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
			path := joinPath(currentPath, key)
			if _, duplicate := result[key]; duplicate {
				return nil, renderError(StageCanonicalization, "duplicate_json_key", path, "JSON object key is repeated", nil)
			}
			value, err := decodeJSONValue(decoder, path)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, fmt.Errorf("object is not closed")
		}
		return result, nil
	case '[':
		result := make([]any, 0)
		for index := 0; decoder.More(); index++ {
			value, err := decodeJSONValue(decoder, fmt.Sprintf("%s[%d]", currentPath, index))
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, fmt.Errorf("array is not closed")
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func Canonicalize(value any) ([]byte, error) {
	var output bytes.Buffer
	if err := writeCanonical(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func CanonicalDigest(formatVersion string, canonical []byte) (domain.SHA256Digest, error) {
	if formatVersion == "" || strings.ContainsRune(formatVersion, '\x00') {
		return "", renderError(StageCanonicalization, "invalid_canonical_version", "", "canonical format version must be non-empty and contain no NUL", nil)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(formatVersion))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(canonical)
	return domain.ParseSHA256Digest(fmt.Sprintf("%x", hash.Sum(nil)))
}

func writeCanonical(output *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		output.WriteString("null")
	case bool:
		output.WriteString(strconv.FormatBool(typed))
	case string:
		return writeJSONString(output, typed)
	case json.Number:
		return writeJSONNumber(output, string(typed))
	case float64:
		return writeFloat(output, typed)
	case float32:
		return writeFloat(output, float64(typed))
	case int:
		if !safeSignedInteger(int64(typed)) {
			return unsafeIntegerError()
		}
		output.WriteString(strconv.Itoa(typed))
	case int8:
		output.WriteString(strconv.FormatInt(int64(typed), 10))
	case int16:
		output.WriteString(strconv.FormatInt(int64(typed), 10))
	case int32:
		output.WriteString(strconv.FormatInt(int64(typed), 10))
	case int64:
		if !safeSignedInteger(typed) {
			return unsafeIntegerError()
		}
		output.WriteString(strconv.FormatInt(typed, 10))
	case uint:
		if !safeUnsignedInteger(uint64(typed)) {
			return unsafeIntegerError()
		}
		output.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint8:
		output.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint16:
		output.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint32:
		output.WriteString(strconv.FormatUint(uint64(typed), 10))
	case uint64:
		if !safeUnsignedInteger(typed) {
			return unsafeIntegerError()
		}
		output.WriteString(strconv.FormatUint(typed, 10))
	case []any:
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeCanonical(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if !utf8.ValidString(key) {
				return renderError(StageCanonicalization, "invalid_utf8", "", "object key is not valid UTF-8", nil)
			}
			keys = append(keys, key)
		}
		sort.Slice(keys, func(left, right int) bool { return utf16Less(keys[left], keys[right]) })
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeJSONString(output, key); err != nil {
				return err
			}
			output.WriteByte(':')
			if err := writeCanonical(output, typed[key]); err != nil {
				return err
			}
		}
		output.WriteByte('}')
	default:
		return renderError(StageCanonicalization, "unsupported_value", "", fmt.Sprintf("cannot canonicalize %T", value), nil)
	}
	return nil
}

const maxSafeInteger = uint64(9_007_199_254_740_991)

func safeSignedInteger(value int64) bool {
	return value >= -int64(maxSafeInteger) && value <= int64(maxSafeInteger)
}

func safeUnsignedInteger(value uint64) bool { return value <= maxSafeInteger }

func unsafeIntegerError() error {
	return renderError(StageCanonicalization, "integer_out_of_safe_range", "", "typed integer is outside the interoperable IEEE-754 safe range", nil)
}

func writeJSONNumber(output *bytes.Buffer, number string) error {
	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return renderError(StageCanonicalization, "invalid_number", "", "number is not an IEEE-754 value", err)
	}
	return writeFloat(output, value)
}

func writeFloat(output *bytes.Buffer, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return renderError(StageCanonicalization, "invalid_number", "", "number must be finite", nil)
	}
	if value == 0 {
		output.WriteByte('0')
		return nil
	}
	negative := value < 0
	if negative {
		value = -value
		output.WriteByte('-')
	}
	raw := strconv.FormatFloat(value, 'e', -1, 64)
	parts := strings.Split(raw, "e")
	if len(parts) != 2 {
		return renderError(StageCanonicalization, "invalid_number", "", "number formatter returned an unexpected representation", nil)
	}
	exponent, err := strconv.Atoi(parts[1])
	if err != nil {
		return renderError(StageCanonicalization, "invalid_number", "", "number exponent is invalid", err)
	}
	digits := strings.ReplaceAll(parts[0], ".", "")
	decimalPosition := exponent + 1
	switch {
	case decimalPosition > 0 && decimalPosition <= 21:
		if decimalPosition >= len(digits) {
			output.WriteString(digits)
			output.WriteString(strings.Repeat("0", decimalPosition-len(digits)))
		} else {
			output.WriteString(digits[:decimalPosition])
			output.WriteByte('.')
			output.WriteString(digits[decimalPosition:])
		}
	case decimalPosition <= 0 && decimalPosition > -6:
		output.WriteString("0.")
		output.WriteString(strings.Repeat("0", -decimalPosition))
		output.WriteString(digits)
	default:
		output.WriteByte(digits[0])
		if len(digits) > 1 {
			output.WriteByte('.')
			output.WriteString(digits[1:])
		}
		output.WriteByte('e')
		scientificExponent := decimalPosition - 1
		if scientificExponent >= 0 {
			output.WriteByte('+')
		}
		output.WriteString(strconv.Itoa(scientificExponent))
	}
	return nil
}

func writeJSONString(output *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) {
		return renderError(StageCanonicalization, "invalid_utf8", "", "string is not valid UTF-8", nil)
	}
	output.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 {
				fmt.Fprintf(output, `\u%04x`, character)
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
	return nil
}

func utf16Less(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	limit := min(len(leftUnits), len(rightUnits))
	for index := 0; index < limit; index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
}

func validateJSONSurrogates(input []byte) error {
	for index := 0; index < len(input); index++ {
		if input[index] != '"' {
			continue
		}
		index++
		for index < len(input) && input[index] != '"' {
			if input[index] != '\\' {
				index++
				continue
			}
			index++
			if index >= len(input) {
				break
			}
			if input[index] != 'u' {
				index++
				continue
			}
			value, next, ok := readUnicodeEscape(input, index)
			if !ok {
				index++
				continue
			}
			if value >= 0xdc00 && value <= 0xdfff {
				return renderError(StageCanonicalization, "lone_unicode_surrogate", "", "JSON contains a lone low surrogate", nil)
			}
			if value >= 0xd800 && value <= 0xdbff {
				if next+1 >= len(input) || input[next] != '\\' || input[next+1] != 'u' {
					return renderError(StageCanonicalization, "lone_unicode_surrogate", "", "JSON contains a lone high surrogate", nil)
				}
				low, lowNext, lowOK := readUnicodeEscape(input, next+1)
				if !lowOK || low < 0xdc00 || low > 0xdfff {
					return renderError(StageCanonicalization, "lone_unicode_surrogate", "", "JSON contains an invalid surrogate pair", nil)
				}
				index = lowNext - 1
				continue
			}
			index = next - 1
		}
	}
	return nil
}

func readUnicodeEscape(input []byte, uIndex int) (uint16, int, bool) {
	if uIndex+5 > len(input) || input[uIndex] != 'u' {
		return 0, uIndex, false
	}
	parsed, err := strconv.ParseUint(string(input[uIndex+1:uIndex+5]), 16, 16)
	if err != nil {
		return 0, uIndex, false
	}
	return uint16(parsed), uIndex + 5, true
}
