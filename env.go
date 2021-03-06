package env

import (
	"encoding"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// nolint: gochecknoglobals
var (
	// ErrNotAStructPtr is returned if you pass something that is not a pointer to a
	// Struct to Parse
	ErrNotAStructPtr = errors.New("env: expected a pointer to a Struct")

	defaultBuiltInParsers = map[reflect.Kind]ParserFunc{
		reflect.Bool: func(v string) (interface{}, error) {
			return strconv.ParseBool(v)
		},
		reflect.String: func(v string) (interface{}, error) {
			return v, nil
		},
		reflect.Int: func(v string) (interface{}, error) {
			i, err := strconv.ParseInt(v, 10, 32)
			return int(i), err
		},
		reflect.Int16: func(v string) (interface{}, error) {
			i, err := strconv.ParseInt(v, 10, 16)
			return int16(i), err
		},
		reflect.Int32: func(v string) (interface{}, error) {
			i, err := strconv.ParseInt(v, 10, 32)
			return int32(i), err
		},
		reflect.Int64: func(v string) (interface{}, error) {
			return strconv.ParseInt(v, 10, 64)
		},
		reflect.Int8: func(v string) (interface{}, error) {
			i, err := strconv.ParseInt(v, 10, 8)
			return int8(i), err
		},
		reflect.Uint: func(v string) (interface{}, error) {
			i, err := strconv.ParseUint(v, 10, 32)
			return uint(i), err
		},
		reflect.Uint16: func(v string) (interface{}, error) {
			i, err := strconv.ParseUint(v, 10, 16)
			return uint16(i), err
		},
		reflect.Uint32: func(v string) (interface{}, error) {
			i, err := strconv.ParseUint(v, 10, 32)
			return uint32(i), err
		},
		reflect.Uint64: func(v string) (interface{}, error) {
			i, err := strconv.ParseUint(v, 10, 64)
			return i, err
		},
		reflect.Uint8: func(v string) (interface{}, error) {
			i, err := strconv.ParseUint(v, 10, 8)
			return uint8(i), err
		},
		reflect.Float64: func(v string) (interface{}, error) {
			return strconv.ParseFloat(v, 64)
		},
		reflect.Float32: func(v string) (interface{}, error) {
			f, err := strconv.ParseFloat(v, 32)
			return float32(f), err
		},
	}

	defaultTypeParsers = map[reflect.Type]ParserFunc{
		reflect.TypeOf(url.URL{}): func(v string) (interface{}, error) {
			u, err := url.Parse(v)
			if err != nil {
				return nil, fmt.Errorf("unable to parse URL: %v", err)
			}
			return *u, nil
		},
		reflect.TypeOf(time.Nanosecond): func(v string) (interface{}, error) {
			s, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("unable to parse duration: %v", err)
			}
			return s, err
		},
	}
)

// ParserFunc defines the signature of a function that can be used within `CustomParsers`
type ParserFunc func(v string) (interface{}, error)

// Parse parses a struct containing `env` tags and loads its values from
// environment variables.
func Parse(v interface{}) error {
	return ParseWithFuncs(v, map[reflect.Type]ParserFunc{})
}

// ParsePrefix parses a struct containing `env` tags and loads its values from
// environment variables. Prefixes evironment variables with prefix
func ParsePrefix(prefix string, v interface{}) error {
	return ParsePrefixWithFuncs(prefix, v, map[reflect.Type]ParserFunc{})
}

// ParseWithFuncs is the same as `Parse` except it also allows the user to pass
// in custom parsers.
func ParseWithFuncs(v interface{}, funcMap map[reflect.Type]ParserFunc) error {
	return ParsePrefixWithFuncs("", v, funcMap)
}

// ParsePrefixWithFuncs is the same as `ParsePrefix` except it also allows the user to pass
// in custom parsers.
func ParsePrefixWithFuncs(prefix string, v interface{}, funcMap map[reflect.Type]ParserFunc) error {
	ptrRef := reflect.ValueOf(v)
	if ptrRef.Kind() != reflect.Ptr {
		return ErrNotAStructPtr
	}
	ref := ptrRef.Elem()
	if ref.Kind() != reflect.Struct {
		return ErrNotAStructPtr
	}
	var parsers = defaultTypeParsers
	for k, v := range funcMap {
		parsers[k] = v
	}
	return doParse(prefix, ref, parsers)
}

func doParse(prefix string, ref reflect.Value, funcMap map[reflect.Type]ParserFunc) error {
	var refType = ref.Type()

	for i := 0; i < refType.NumField(); i++ {
		refField := ref.Field(i)
		if !refField.CanSet() {
			continue
		}
		if reflect.Ptr == refField.Kind() && !refField.IsNil() {
			envPrefix := refType.Field(i).Tag.Get("envPrefix")
			err := ParsePrefixWithFuncs(prefix+envPrefix, refField.Interface(), funcMap)
			if err != nil {
				return err
			}
			continue
		}
		if reflect.Struct == refField.Kind() && refField.CanAddr() && refField.Type().Name() == "" {
			envPrefix := refType.Field(i).Tag.Get("envPrefix")
			err := ParsePrefix(prefix+envPrefix, refField.Addr().Interface())
			if err != nil {
				return err
			}
			continue
		}
		refTypeField := refType.Field(i)
		value, err := get(prefix, refTypeField)
		if err != nil {
			return err
		}
		if value == "" {
			if reflect.Struct == refField.Kind() {
				envPrefix := refType.Field(i).Tag.Get("envPrefix")
				if err := doParse(prefix+envPrefix, refField, funcMap); err != nil {
					return err
				}
			}
			continue
		}
		if err := set(refField, refTypeField, value, funcMap); err != nil {
			return err
		}
	}
	return nil
}

func get(prefix string, field reflect.StructField) (val string, err error) {
	var required bool
	var exists bool
	var loadFile bool
	var expand = strings.EqualFold(field.Tag.Get("envExpand"), "true")

	key, opts := parseKeyForOption(field.Tag.Get("env"))

	for _, opt := range opts {
		switch opt {
		case "":
			break
		case "file":
			loadFile = true
		case "required":
			required = true
		default:
			return "", fmt.Errorf("env: tag option %q not supported", opt)
		}
	}

	defaultValue := field.Tag.Get("envDefault")
	val, exists = getOr(prefix, key, defaultValue)

	if expand {
		val = os.ExpandEnv(val)
	}

	if required && !exists {
		return "", fmt.Errorf(`env: required environment variable %q is not set`, key)
	}

	if loadFile && val != "" {
		filename := val
		val, err = getFromFile(filename)
		if err != nil {
			return "", fmt.Errorf(`env: could not load content of file "%s" from variable %s: %v`, filename, key, err)
		}
	}

	return val, err
}

// split the env tag's key into the expected key and desired option, if any.
func parseKeyForOption(key string) (string, []string) {
	opts := strings.Split(key, ",")
	return opts[0], opts[1:]
}

func getFromFile(filename string) (value string, err error) {
	b, err := ioutil.ReadFile(filename)
	return string(b), err
}

func getOr(prefix, key, defaultValue string) (value string, exists bool) {
	value, exists = os.LookupEnv(prefix + key)
	if !exists {
		value = defaultValue
	}
	return value, exists
}

func set(field reflect.Value, sf reflect.StructField, value string, funcMap map[reflect.Type]ParserFunc) error {
	if field.Kind() == reflect.Slice {
		return handleSlice(field, value, sf, funcMap)
	}

	var tm = asTextUnmarshaler(field)
	if tm != nil {
		var err = tm.UnmarshalText([]byte(value))
		return newParseError(sf, err)
	}

	var typee = sf.Type
	var fieldee = field
	if typee.Kind() == reflect.Ptr {
		typee = typee.Elem()
		fieldee = field.Elem()
	}

	parserFunc, ok := funcMap[typee]
	if ok {
		val, err := parserFunc(value)
		if err != nil {
			return newParseError(sf, err)
		}

		fieldee.Set(reflect.ValueOf(val))
		return nil
	}

	parserFunc, ok = defaultBuiltInParsers[typee.Kind()]
	if ok {
		val, err := parserFunc(value)
		if err != nil {
			return newParseError(sf, err)
		}

		fieldee.Set(reflect.ValueOf(val).Convert(typee))
		return nil
	}

	return newNoParserError(sf)
}

func handleSlice(field reflect.Value, value string, sf reflect.StructField, funcMap map[reflect.Type]ParserFunc) error {
	var separator = sf.Tag.Get("envSeparator")
	if separator == "" {
		separator = ","
	}
	var parts = strings.Split(value, separator)

	var typee = sf.Type.Elem()
	if typee.Kind() == reflect.Ptr {
		typee = typee.Elem()
	}

	if _, ok := reflect.New(typee).Interface().(encoding.TextUnmarshaler); ok {
		return parseTextUnmarshalers(field, parts, sf)
	}

	parserFunc, ok := funcMap[typee]
	if !ok {
		parserFunc, ok = defaultBuiltInParsers[typee.Kind()]
		if !ok {
			return newNoParserError(sf)
		}
	}

	var result = reflect.MakeSlice(sf.Type, 0, len(parts))
	for _, part := range parts {
		r, err := parserFunc(part)
		if err != nil {
			return newParseError(sf, err)
		}
		var v = reflect.ValueOf(r).Convert(typee)
		if sf.Type.Elem().Kind() == reflect.Ptr {
			v = reflect.New(typee)
			v.Elem().Set(reflect.ValueOf(r).Convert(typee))
		}
		result = reflect.Append(result, v)
	}
	field.Set(result)
	return nil
}

func asTextUnmarshaler(field reflect.Value) encoding.TextUnmarshaler {
	if reflect.Ptr == field.Kind() {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
	} else if field.CanAddr() {
		field = field.Addr()
	}

	tm, ok := field.Interface().(encoding.TextUnmarshaler)
	if !ok {
		return nil
	}
	return tm
}

func parseTextUnmarshalers(field reflect.Value, data []string, sf reflect.StructField) error {
	s := len(data)
	elemType := field.Type().Elem()
	slice := reflect.MakeSlice(reflect.SliceOf(elemType), s, s)
	for i, v := range data {
		sv := slice.Index(i)
		kind := sv.Kind()
		if kind == reflect.Ptr {
			sv = reflect.New(elemType.Elem())
		} else {
			sv = sv.Addr()
		}
		tm := sv.Interface().(encoding.TextUnmarshaler)
		if err := tm.UnmarshalText([]byte(v)); err != nil {
			return newParseError(sf, err)
		}
		if kind == reflect.Ptr {
			slice.Index(i).Set(sv)
		}
	}

	field.Set(slice)

	return nil
}

func newParseError(sf reflect.StructField, err error) error {
	if err == nil {
		return nil
	}
	return parseError{
		sf:  sf,
		err: err,
	}
}

type parseError struct {
	sf  reflect.StructField
	err error
}

func (e parseError) Error() string {
	return fmt.Sprintf(`env: parse error on field "%s" of type "%s": %v`, e.sf.Name, e.sf.Type, e.err)
}

func newNoParserError(sf reflect.StructField) error {
	return fmt.Errorf(`env: no parser found for field "%s" of type "%s"`, sf.Name, sf.Type)
}
