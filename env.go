// Package env allows you to load configuration from environment variables into
// Go structures of your own choosing.
package env

import (
	"encoding"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	tt "text/template"
	"time"
	"unicode"
)

// parseFunc takes a string and coerces it into some target type. If coercion
// fails, an error is returned.
type parseFunc func(s string) (interface{}, error)

var (
	errInvalidDst    = errors.New("dst must be struct or struct pointer")
	errUnexportedDst = errors.New("cannot write unexported field")
)

type loadError struct {
	errs []error
}

func (e *loadError) Error() string {
	errStr := "env: cannot load environment config: "
	for i, err := range e.errs {
		if i > 0 {
			errStr += ", "
		}
		errStr += err.Error()
	}
	return errStr
}

// Load will load configuration from environment to dst, which must be a struct
// or a struct pointer.
func Load(dst interface{}, prefix string) error {
	return newLoader().Load(dst, prefix)
}

// loader is used to load the environment.
type loader struct {
	parsers map[reflect.Type]parseFunc
}

// newLoader returns a loader with a default set of parsers.
func newLoader() *loader {
	return &loader{defaultParsers()}
}

// Load will load configuration from environment to dst, which must be a struct
// or a struct pointer.
func (l *loader) Load(dst interface{}, prefix string) error {
	errs := l.loadStruct(reflect.ValueOf(dst), prefix)
	if len(errs) > 0 {
		return &loadError{errs}
	}
	return nil
}

// AddParser will register a custom parser f which will be used to load all
// instances of rt from environment.
func (l *loader) AddParser(rt reflect.Type, f parseFunc) {
	l.parsers[rt] = f
}

func (l *loader) hasParser(rt reflect.Type) bool {
	_, ok := l.parsers[rt]
	return ok
}

func isExported(f reflect.StructField) bool {
	for _, r := range f.Name {
		return unicode.IsUpper(r)
	}
	panic("bug: f.Name cannot be empty")
}

func (l *loader) loadStruct(rv reflect.Value, prefix string) []error {
	rv = follow(rv)
	if rv.Kind() != reflect.Struct || !rv.CanAddr() {
		return []error{errInvalidDst}
	}
	var errs []error
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		// When the field has no env tag, we don't touch it at all.
		// The only exception is anonymous structs to which we recurse.
		tag, hasTag := f.Tag.Lookup("env")
		isStruct := f.Type.Kind() == reflect.Struct
		isAnonStruct := isStruct && f.Anonymous
		if !hasTag && !isAnonStruct {
			continue
		}
		if !isExported(f) {
			err := fmt.Errorf("%q: %w", f.Name, errUnexportedDst)
			errs = append(errs, err)
			continue
		}
		fv := rv.Field(i)
		if !fv.CanInterface() || !fv.CanSet() {
			err := fmt.Errorf("%q: %w", f.Name, errInvalidDst)
			errs = append(errs, err)
			continue
		}
		name := prefix + tag
		isTU := (textUnmarshaler(fv) != nil)
		hasParser := l.hasParser(f.Type)
		if isStruct && !hasParser && !isTU {
			// Recurse to the field which is a structure.
			errs = append(errs, l.loadStruct(fv, name)...)
		} else if err := l.loadVar(fv, name); err != nil {
			errs = append(errs, fmt.Errorf("%q: %w", name, err))
		}
	}
	return errs
}

func (l *loader) loadVar(rv reflect.Value, name string) error {
	if !l.hasParser(rv.Type()) {
		rv = follow(rv)
	}
	tu := textUnmarshaler(rv)
	if (tu == nil) && rv.Kind() == reflect.Map {
		if err := l.parseAndSetMap(name, rv); err != nil {
			return fmt.Errorf("cannot parse %s: %w", rv.Type(), err)
		}
		return nil
	}
	s, ok := os.LookupEnv(name)
	if !ok {
		return errors.New("variable missing")
	}
	if err := l.parseAndSetValue(s, rv); err != nil {
		rt := rv.Type()
		return fmt.Errorf("cannot parse %q as %s: %w", s, rt, err)
	}
	return nil
}

func (l *loader) parseAndSetValue(s string, rv reflect.Value) error {
	if tu := textUnmarshaler(rv); tu != nil {
		return tu.UnmarshalText([]byte(s))
	}
	rt := rv.Type()
	if f := l.parsers[rt]; f != nil {
		v, err := f(s)
		if err == nil {
			rv.Set(reflect.ValueOf(v))
		}
		return err
	}
	if rt.Kind() == reflect.Slice {
		return l.parseAndSetSlice(s, rv)
	}
	return fmt.Errorf("parsing of %v not supported", rt)
}

func tokenizeSliceString(s string) ([]string, error) {
	var q, esc bool
	var sb strings.Builder
	var fields []string
	for _, r := range s {
		if r == ',' && !esc && !q {
			str := sb.String()
			if len(str) == 0 {
				msg := "empty fields must be quoted"
				return nil, fmt.Errorf(msg)
			}
			fields = append(fields, str)
			sb.Reset()
		} else {
			sb.WriteRune(r)
			if r == '"' && !esc {
				q = !q
			} else if r == '\\' && !esc {
				esc = true
			} else if r == '\x00' {
				return nil, fmt.Errorf("NUL byte in input")
			} else {
				esc = false
			}
		}
	}
	if sb.Len() > 0 {
		fields = append(fields, sb.String())
	}
	if q {
		return nil, fmt.Errorf("unbalanced quotes")
	}
	if esc {
		return nil, fmt.Errorf("trailing \\")
	}
	return fields, nil
}

func unescapeSliceField(f string) string {
	runes := []rune(f)

	// Trim trailing spaces (find end of the string).
	end := len(runes) - 1
	for ; end >= 0; end-- {
		if unicode.IsSpace(runes[end]) {
			continue
		} else if runes[end] == '\\' {
			end++
		}
		break
	}

	// Trim leading spaces (find start of the string).
	start := 0
	for ; start < len(runes) && unicode.IsSpace(runes[start]); start++ {
	}

	// Extract and unescape the string.
	var sb strings.Builder
	for i := start; i <= end; i++ {
		if runes[i] == '\\' {
			i++
			sb.WriteRune(runes[i])
		} else if runes[i] != '"' {
			sb.WriteRune(runes[i])
		}
	}
	return sb.String()
}

// parseAndSetSlice parses a comma-separated list of values as a slice.
func (l *loader) parseAndSetSlice(s string, rv reflect.Value) error {
	fields, err := tokenizeSliceString(s)
	if err != nil {
		return err
	}
	for i, f := range fields {
		fields[i] = unescapeSliceField(f)
	}
	nfield := len(fields)
	sl := reflect.MakeSlice(rv.Type(), nfield, nfield)
	for i, s := range fields {
		if err := l.parseAndSetValue(s, sl.Index(i)); err != nil {
			return fmt.Errorf("item #%d: %w", i, err)
		}
	}
	rv.Set(sl)
	return nil
}

func varsPrefixed(prefix string) map[string]string {
	vars := make(map[string]string)
	for _, ev := range os.Environ() {
		spl := strings.SplitN(ev, "=", 2)
		name, value := spl[0], spl[1]
		if strings.HasPrefix(name, prefix) {
			// keys should be unique, as EnvVars are
			vars[name] = value
		}
	}
	return vars
}

func (l *loader) parseAndSetMap(mapName string, rv reflect.Value) error {
	rt := rv.Type()
	kt, vt := rt.Key(), rt.Elem()
	dstMap := reflect.MakeMap(rt)

	for varName, valStr := range varsPrefixed(mapName) {
		keyStr := varName[len(mapName):]
		key := reflect.New(kt).Elem() // New creates a pointer
		if err := l.parseAndSetValue(keyStr, follow(key)); err != nil {
			msg := "parsing string %q as the key (%s) failed: %w"
			return fmt.Errorf(msg, keyStr, kt, err)
		}

		val := reflect.New(vt).Elem() // New creates a pointer
		if err := l.parseAndSetValue(valStr, follow(val)); err != nil {
			msg := "parsing string %q as the value (%s) failed: %w"
			return fmt.Errorf(msg, valStr, vt, err)
		}

		dstMap.SetMapIndex(key, val)
	}

	rv.Set(dstMap)
	return nil
}

// follow will follow all pointer indirections in rv, creating destinations as
// needed. For example, if rv is **T which points to *T which is nil, follow
// will set the *T to point to a new T initialized to the zero value.
func follow(rv reflect.Value) reflect.Value {
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rn := reflect.New(rv.Type().Elem())
			rv.Set(rn)
			rv = rn
		}
		rv = rv.Elem()
	}
	return rv
}

func textUnmarshaler(rv reflect.Value) encoding.TextUnmarshaler {
	if tu, ok := rv.Interface().(encoding.TextUnmarshaler); ok {
		return tu
	}
	// Don't give up yet; value of rv is not a TextUnmarshaler but
	// a pointer to it may be.
	if !rv.CanAddr() {
		return nil
	}
	rv = rv.Addr()
	if tu, ok := rv.Interface().(encoding.TextUnmarshaler); ok {
		return tu
	}
	return nil
}

func defaultParsers() map[reflect.Type]parseFunc {
	return map[reflect.Type]parseFunc{
		reflect.TypeOf(bool(false)):      parseBool,
		reflect.TypeOf(os.FileMode(0)):   parseFileMode,
		reflect.TypeOf(float32(0)):       parseFloat32,
		reflect.TypeOf(float64(0)):       parseFloat64,
		reflect.TypeOf(int(0)):           parseInt,
		reflect.TypeOf(uint(0)):          parseUint,
		reflect.TypeOf(int8(0)):          parseInt8,
		reflect.TypeOf(uint8(0)):         parseUint8,
		reflect.TypeOf(int16(0)):         parseInt16,
		reflect.TypeOf(uint16(0)):        parseUint16,
		reflect.TypeOf(int32(0)):         parseInt32,
		reflect.TypeOf(uint32(0)):        parseUint32,
		reflect.TypeOf(int64(0)):         parseInt64,
		reflect.TypeOf(uint64(0)):        parseUint64,
		reflect.TypeOf(string("")):       parseString,
		reflect.TypeOf(regexp.Regexp{}):  parseRegex,
		reflect.TypeOf(time.Duration(0)): parseDuration,
		reflect.TypeOf(url.URL{}):        parseURL,
		reflect.TypeOf(tt.Template{}):    parseTextTemplate,
	}
}

func parseBool(s string) (interface{}, error) {
	return strconv.ParseBool(s)
}

func parseFileMode(s string) (interface{}, error) {
	if len(s) > 0 && s[0] != '0' {
		return nil, fmt.Errorf("file mode must be prefixed with 0")
	}
	val, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return nil, err
	}
	return os.FileMode(val), nil
}

func parseFloat32(s string) (interface{}, error) {
	val, err := strconv.ParseFloat(s, 32)
	return float32(val), err
}

func parseFloat64(s string) (interface{}, error) {
	return strconv.ParseFloat(s, 64)
}

func parseInt(s string) (interface{}, error) {
	return strconv.Atoi(s)
}

func parseUint(s string) (interface{}, error) {
	val, err := strconv.ParseUint(s, 10, strconv.IntSize)
	return uint(val), err
}

func parseInt8(s string) (interface{}, error) {
	val, err := strconv.ParseInt(s, 10, 8)
	return int8(val), err
}

func parseUint8(s string) (interface{}, error) {
	val, err := strconv.ParseUint(s, 10, 8)
	return uint8(val), err
}

func parseInt16(s string) (interface{}, error) {
	val, err := strconv.ParseInt(s, 10, 16)
	return int16(val), err
}

func parseUint16(s string) (interface{}, error) {
	val, err := strconv.ParseUint(s, 10, 16)
	return uint16(val), err
}

func parseInt32(s string) (interface{}, error) {
	val, err := strconv.ParseInt(s, 10, 32)
	return int32(val), err
}

func parseUint32(s string) (interface{}, error) {
	val, err := strconv.ParseUint(s, 10, 32)
	return uint32(val), err
}

func parseInt64(s string) (interface{}, error) {
	return strconv.ParseInt(s, 10, 64)
}

func parseUint64(s string) (interface{}, error) {
	return strconv.ParseUint(s, 10, 64)
}

func parseString(s string) (interface{}, error) {
	return s, nil
}

func parseRegex(s string) (interface{}, error) {
	r, err := regexp.Compile(s)
	if err != nil {
		return nil, err
	}
	return *r, nil
}

func parseDuration(s string) (interface{}, error) {
	return time.ParseDuration(s)
}

func parseURL(s string) (interface{}, error) {
	url, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	return *url, nil
}

func parseTextTemplate(s string) (interface{}, error) {
	t, err := tt.New("from_env").Parse(s)
	if err != nil {
		return nil, err
	}
	return *t, nil
}
