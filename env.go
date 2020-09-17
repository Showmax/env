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
// or a struct pointer, using the default loader.
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
	if rv.Kind() == reflect.Map {
		if err := l.parseAndSetMap(name, rv); err != nil {
			return fmt.Errorf("can't parse %s: %w", rv.Type(), err)
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
	rt := rv.Type()
	if f := l.parsers[rt]; f != nil {
		v, err := f(s)
		if err == nil {
			rv.Set(reflect.ValueOf(v))
		}
		return err
	}
	if tu := textUnmarshaler(rv); tu != nil {
		return tu.UnmarshalText([]byte(s))
	}
	if rt.Kind() == reflect.Slice {
		return l.parseAndSetSlice(s, rv)
	}
	return fmt.Errorf("parsing of %v not supported", rt)
}

// parseAndSetSlice parses a comma-separated list of values as a slice.
func (l *loader) parseAndSetSlice(s string, rv reflect.Value) error {
	var q, esc bool
	var sb strings.Builder
	var fields []string
	for _, r := range s {
		if r == ',' && !esc && !q {
			fields = append(fields, sb.String())
			sb.Reset()
		} else {
			sb.WriteRune(r)
			if r == '"' && !esc {
				q = !q
			} else if r == '\\' && !esc {
				esc = true
			} else if r == '\x00' {
				return fmt.Errorf("NUL byte in input")
			} else {
				esc = false
			}
		}
	}
	if sb.Len() > 0 {
		fields = append(fields, sb.String())
	}
	if q {
		return fmt.Errorf("unbalanced quotes")
	}
	if esc {
		return fmt.Errorf("trailing \\")
	}
	for i, f := range fields {
		if len(f) == 0 {
			return fmt.Errorf("empty fields must be quoted")
		}
		f = strings.TrimSpace(f)
		f = strings.ReplaceAll(f, `\"`, "\x00")
		f = strings.ReplaceAll(f, `"`, "")
		f = strings.ReplaceAll(f, "\x00", `"`)
		f = strings.ReplaceAll(f, `\\`, "\x00")
		f = strings.ReplaceAll(f, `\`, "")
		f = strings.ReplaceAll(f, "\x00", `\`)
		f = strings.ReplaceAll(f, `\,`, `,`)
		fields[i] = f
	}

	// Set slice values. Skip last field.
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

func varsPrefixed(pfx string) map[string]string {
	vars := make(map[string]string)
	for _, ev := range os.Environ() {
		spl := strings.SplitN(ev, "=", 2)
		name, value := spl[0], spl[1]
		if strings.HasPrefix(name, pfx) {
			vars[name] = value // keys are be unique, as EnvVars are
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
		reflect.TypeOf(float64(0)):       parseFloat64,
		reflect.TypeOf(int(0)):           parseInt,
		reflect.TypeOf(int64(0)):         parseInt64,
		reflect.TypeOf(uint64(0)):        parseUint64,
		reflect.TypeOf(string("")):       parseString,
		reflect.TypeOf(&regexp.Regexp{}): parseRegex,
		reflect.TypeOf(time.Duration(0)): parseDuration,
		reflect.TypeOf(&url.URL{}):       parseURL,
		reflect.TypeOf(&tt.Template{}):   parseTextTemplate,
	}
}

func parseBool(s string) (interface{}, error) {
	return strconv.ParseBool(s)
}

func parseInt(s string) (interface{}, error) {
	return strconv.Atoi(s)
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

func parseFloat64(s string) (interface{}, error) {
	return strconv.ParseFloat(s, 64)
}

func parseRegex(s string) (interface{}, error) {
	return regexp.Compile(s)
}

func parseDuration(s string) (interface{}, error) {
	return time.ParseDuration(s)
}

func parseURL(s string) (interface{}, error) {
	return url.Parse(s)
}

func parseTextTemplate(s string) (interface{}, error) {
	return tt.New("from_env").Parse(s)
}
