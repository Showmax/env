package env

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	tt "text/template"
	"time"

	"github.com/stretchr/testify/assert"
)

type Foo struct {
	Foo string `env:"FOO"`
}

type Bar struct {
	Bar string `env:"BAR"`
}

type config struct {
	Foo
	Bar         `env:"BAR_"`
	Bool        *bool         `env:"BOOL"`
	Duration    time.Duration `env:"DURATION"`
	Inner       Foo           `env:"INNER_"`
	Int         int           `env:"INT"`
	IntSlice    *[]int        `env:"INT_SLICE"`
	String      string        `env:"STRING"`
	StringSlice []string      `env:"STRING_SLICE"`
	URLValue    url.URL       `env:"URL_VALUE"`
	URLPtr      *url.URL      `env:"URL_PTR"`
	Regexp      regexp.Regexp `env:"REGEXP"`
	Template    tt.Template   `env:"TEMPLATE"`
	badConfig   int           //nolint:structcheck,unused
}

type badConfig struct {
	//nolint:structcheck,unused
	foo Foo `env:"FOO"` // trying to load badConfig field!
}

const examplePrefix = "PREFIX_"

type environment map[string]string

func (e environment) dup() environment {
	f := make(environment, len(e))
	for k, v := range e {
		f[k] = v
	}
	return f
}

//nolint:gochecknoglobals
var (
	goodEnv = environment{
		"BAR_BAR":      "BAR_BAR",
		"FOO":          "FOO",
		"BOOL":         "true",
		"DURATION":     "10ms",
		"INNER_FOO":    "INNER_FOO",
		"INT":          "1",
		"INT_SLICE":    `1,2,"3"`,
		"STRING":       "STRING",
		"STRING_SLICE": `"comma separated",values`,
		"URL_VALUE":    "https://example.org",
		"URL_PTR":      "https://example.org",
		"REGEXP":       "^[a-c]bbf*d+c*$",
		"TEMPLATE":     "{{23 -}} < {{- 45}}",
	}
	invalidVars = environment{
		"BOOL":         "flase",
		"DURATION":     "10d",
		"INT":          "2.2",
		"INT_SLICE":    "1,2,2.5,3",
		"STRING_SLICE": `"missing","one","quote`,
	}
	trueVar    = true
	goodConfig = config{
		Foo:         Foo{"FOO"},
		Bar:         Bar{"BAR_BAR"},
		Bool:        &trueVar,
		Duration:    10 * time.Millisecond,
		Inner:       Foo{"INNER_FOO"},
		Int:         1,
		IntSlice:    &[]int{1, 2, 3},
		String:      "STRING",
		StringSlice: []string{"comma separated", "values"},
		URLValue:    url.URL{Scheme: "https", Host: "example.org"},
		URLPtr:      &url.URL{Scheme: "https", Host: "example.org"},
		Regexp:      *regexp.MustCompile("^[a-c]bbf*d+c*$"),
		Template: func() tt.Template {
			t, err := tt.New("from_env").Parse("{{23 -}} < {{- 45}}")
			if err != nil {
				panic(err.Error())
			}
			return *t
		}(),
	}
)

func setenv(env environment) {
	os.Clearenv()
	for k, v := range env {
		if err := os.Setenv("PREFIX_"+k, v); err != nil {
			panic("bug: os.Setenv: " + err.Error())
		}
	}
}

// TestLoadOK tests that a correct environment (containing all the variables
// with valid values for their respective types) doesn't fail and produces
// a config which contains the values from the environment.
func TestLoadOK(t *testing.T) {
	a := assert.New(t)

	var cfg config
	setenv(goodEnv)

	err := Load(&cfg, examplePrefix)
	a.NoError(err, "loading correct env failed")
	a.Equal(goodConfig, cfg)
}

// TestLoadMissingVar will try to remove each variable from goodEnv one by one.
// We expect to get an error.
func TestLoadMissingVar(t *testing.T) {
	a := assert.New(t)

	var cfg config
	for k := range goodEnv {
		oneMissing := goodEnv.dup()
		delete(oneMissing, k)
		setenv(oneMissing)

		err := Load(&cfg, examplePrefix)
		a.Error(err, "loading env with %q missing should fail", k)
	}
}

// TestLoadInvalidVar sets each variable to an invalid value in turn and tests
// that the resulting environment is reported as invalid. We don't test all the
// combinations of bad inputs and input types since we're using library parsing
// routines anyway. Therefore, this test only checks that the errors from those
// parsing routines are handled correctly for each type and one of its possible
// invalid inputs.
func TestLoadInvalidVar(t *testing.T) {
	a := assert.New(t)

	var cfg config
	for k, v := range invalidVars {
		oneInvalid := goodEnv.dup()
		oneInvalid[k] = v
		setenv(oneInvalid)

		err := Load(&cfg, examplePrefix)
		a.Error(err, "loading env with invalid %q should fail", k)
	}

}

func TestSlice(t *testing.T) {
	a := assert.New(t)

	samples := map[string][]string{
		`"x"`:             {"x"},
		``:                {},
		`a`:               {`a`},
		`a,`:              {`a`},
		`a,b`:             {`a`, `b`},
		`a, b`:            {`a`, `b`},
		`a,b, "c,d"`:      {`a`, `b`, `c,d`},
		`\"x\"`:           {`"x"`},
		`"\"x\""`:         {`"x"`},
		`\\\"\,x`:         {`\",x`},
		`"",`:             {``},
		`\"`:              {`"`},
		"x\n\ty":          {"x\n\ty"},
		` " x " `:         {` x `},
		`" fo\\\"o ",bar`: {` fo\"o `, `bar`},
		`""","`:           {`,`},
		`a\,b,c`:          {"a,b", "c"},
		`abc,def,"gh,i", jkl ," mno pqr ",s"t uv", " wxy","", ` +
			`,\"あ"鋸",a\,bc,"fo\"o"\,o,"aa"`: {
			`abc`,
			`def`,
			`gh,i`,
			`jkl`,
			` mno pqr `,
			`st uv`,
			` wxy`,
			``,
			``,
			`"あ鋸`,
			`a,bc`,
			`fo"o,o`,
			`aa`,
		},
		`\?`:           {`?`},
		`"foo\\"`:      {`foo\`},
		`foo \\\\ bar`: {`foo \\ bar`},
		`  "" ,`:       {``},
		`  " " ,`:      {` `},
	}
	type cfg struct {
		Slice []string `env:"SLICE"`
	}
	var c cfg
	for s, refOut := range samples {
		os.Setenv("SLICE", s)

		err := Load(&c, "")
		a.NoError(err)
		a.Equal(refOut, c.Slice)
	}
}

func TestSliceBad(t *testing.T) {
	a := assert.New(t)

	samples := []string{
		`,`,
		`"`,
		`"x""`,
		`\`,
		`"x,`,
	}
	type cfg struct {
		Slice []string `env:"SLICE"`
	}
	var c cfg
	for _, s := range samples {
		os.Setenv("SLICE", s)

		err := Load(&c, "")
		a.Error(err)
	}
}

// TestLoadUnexported tries to load good environment into a structure with an
// badConfig field. That should fail, but it should not panic.
func TestLoadUnexported(t *testing.T) {
	a := assert.New(t)

	var cfg badConfig
	setenv(goodEnv)

	err := Load(&cfg, examplePrefix)
	a.Error(err, "loading badConfig struct should fail")
}

func ExampleLoad() {
	// These variables will come from the environment.
	os.Setenv("EXAMPLE_FOO", "42")
	os.Setenv("EXAMPLE_BAR", "orange")
	os.Setenv("EXAMPLE_FOOBAR", "foobar")

	type config struct {
		Foo    int    `env:"FOO"`
		Bar    string `env:"BAR"`
		Foobar string // No env tag, won't be loaded.
	}

	var c config
	if err := Load(&c, "EXAMPLE_"); err != nil {
		panic(err)
	}
	fmt.Println(c.Foo, c.Bar, c.Foobar)
	// Output: 42 orange
}

func ExampleLoad_missing() {
	type config struct {
		Foo string `env:"FOO"`
	}

	var c config

	// There's nothing like "optional variables" or "defaults". Defaults
	// in configuration are evil. This will blow up.
	os.Clearenv()
	fmt.Println(Load(&c, ""))
	// Output: env: cannot load environment config: "FOO": variable missing
}

func ExampleLoad_nesting() {
	// These variables will come from the environment.
	os.Setenv("EXAMPLE_ADDR", "localhost:1234")
	os.Setenv("EXAMPLE_DB_USER", "joe")
	os.Setenv("EXAMPLE_DB_PASS", "joetherollingstone")

	type dbConfig struct {
		User string `env:"USER"`
		Pass string `env:"PASS"`
	}

	type config struct {
		DB   dbConfig `env:"DB_"` // Note the _.
		Addr string   `env:"ADDR"`
	}

	var c config
	if err := Load(&c, "EXAMPLE_"); err != nil {
		panic(err)
	}
	fmt.Println(c.Addr, c.DB.User, c.DB.Pass)
	// Output: localhost:1234 joe joetherollingstone
}

func ExampleLoad_shared() {
	// These variables will come from the environment.
	os.Setenv("EXAMPLE_LOG_LEVEL", "debug")
	os.Setenv("EXAMPLE_FOO", "foo")

	type SharedConfig struct {
		LogLevel string `env:"LOG_LEVEL"`
	}

	type config struct {
		// Anonymous nested structures are visited. This way it's easy
		// to share some configuration options in all services.
		SharedConfig
		Foo string `env:"FOO"`
	}

	var c config
	if err := Load(&c, "EXAMPLE_"); err != nil {
		panic(err)
	}
	fmt.Println(c.LogLevel, c.Foo)
	// Output: debug foo
}

func TestMapStrings(t *testing.T) {
	a := assert.New(t)

	samples := []map[string]string{
		{
			"a": "A",
			"b": "B",
			"c": "some string",
		},
		{
			"a b c": "A B C",
			"a:b:c": "A/B\\C",
			"a,b,c": "A=B=C",
			"a\"b":  "A\"B",
		},
		{
			"":     "empty key",
			"\n":   "newline\nin\nname",
			"a\rb": "variable_hidden\rab=ab",
			"\\":   "slash_var",
		},
	}
	type cfg struct {
		Map map[string]string `env:"MAP_"`
	}
	for _, ref := range samples {
		for k, v := range ref {
			os.Setenv("MAP_"+k, v)
		}

		var c cfg

		err := Load(&c, "")
		a.NoError(err)
		a.Equal(ref, c.Map)

		for k := range ref {
			os.Unsetenv("MAP_" + k)
		}
	}
}

func TestMapIntKeys(t *testing.T) {
	a := assert.New(t)

	samples := []map[int]string{
		{
			1:  "one",
			2:  "two",
			-5: "minus five",
			0:  "zero",
		},
	}
	type cfg struct {
		Map map[int]string `env:"MAP_"`
	}
	for _, ref := range samples {
		for k, v := range ref {
			os.Setenv("MAP_"+strconv.Itoa(k), v)
		}

		var c cfg

		err := Load(&c, "")
		a.NoError(err)
		a.Equal(ref, c.Map)

		for k := range ref {
			os.Unsetenv("MAP_" + strconv.Itoa(k))
		}
	}
}

func TestMapFloatKeys(t *testing.T) {
	a := assert.New(t)

	samples := []map[float64]string{
		{
			1.0: "one",
			.25: "decimal",
			0:   "zero",
		},
		{
			math.Pow(0.1, 20): "Exp form",
			math.Inf(1):       "+inf",
			math.Inf(-1):      "-inf",
		},
	}
	type cfg struct {
		Map map[float64]string `env:"MAP_"`
	}
	for _, ref := range samples {
		for k, v := range ref {
			os.Setenv("MAP_"+fmt.Sprintf("%g", k), v)
		}

		var c cfg
		err := Load(&c, "")

		a.NoError(err)
		a.Equal(ref, c.Map)

		for k := range ref {
			os.Unsetenv("MAP_" + fmt.Sprintf("%g", k))
		}
	}
}

func TestMapArrVals(t *testing.T) {
	a := assert.New(t)

	samples := []map[string][]string{
		{
			"a": {"A", "B", "C"},
			"b": {},
			"c": {"=bc", "=ef"},
		},
		{
			"a": {"A,B", "C"},
			"":  {"", "", ""},
			"c": {"a\nb\rc", "a\n\n\na", ",,,\n,,,"},
		},
	}
	type cfg struct {
		Map map[string][]string `env:"MAP_"`
	}
	for _, ref := range samples {
		for k, vals := range ref {
			ev := make([]string, 0)
			// Escape array values - commas
			for _, v := range vals {
				v = strings.ReplaceAll(v, ",", "\\,")
				if len(v) == 0 {
					v = "\"" + v + "\""
				}
				ev = append(ev, v)
			}
			os.Setenv("MAP_"+k, strings.Join(ev, ","))
		}

		var c cfg

		err := Load(&c, "")
		a.NoError(err)
		a.Equal(ref, c.Map)

		for k := range ref {
			os.Unsetenv("MAP_" + k)
		}
	}
}

func TestMapPtrs(t *testing.T) {
	a := assert.New(t)

	x := "x"
	y := "y"
	X := "X"
	Z := "Z"
	empty := ""
	samples := []map[*string]*string{
		{
			&x:     &X,
			&y:     &y,
			&empty: &Z,
		},
	}

	type cfg struct {
		Map map[*string]*string `env:"MAP_"`
	}

	// *string aren't comparable, just drop them
	deptr := func(m map[*string]*string) map[string]*string {
		ret := make(map[string]*string, len(m))
		for k, v := range m {
			ret[*k] = v
		}
		return ret
	}

	for _, ref := range samples {
		for k, v := range ref {
			os.Setenv("MAP_"+*k, *v)
		}

		var c cfg

		err := Load(&c, "")
		a.NoError(err)

		dRef := deptr(ref)
		dMap := deptr(c.Map)
		a.Equal(dRef, dMap)

		for k := range ref {
			os.Unsetenv("MAP_" + *k)
		}
	}
}

func TestMapDurations(t *testing.T) {
	a := assert.New(t)

	samples := []map[time.Duration]time.Duration{
		{
			1 * time.Second:       1 * time.Minute,
			0 * time.Microsecond:  2 * time.Minute,
			-1 * time.Hour:        1 * time.Nanosecond,
			-2 * time.Millisecond: 2 * time.Minute,
		},
	}
	type cfg struct {
		Map *map[time.Duration]time.Duration `env:"MAP_"`
	}
	for _, ref := range samples {
		for k, v := range ref {
			os.Setenv("MAP_"+k.String(), v.String())
		}

		var c cfg

		err := Load(&c, "")
		a.NoError(err)
		a.Equal(ref, *c.Map)

		for k := range ref {
			os.Unsetenv("MAP_" + k.String())
		}
	}
}

type customMap map[string]string

func (m *customMap) UnmarshalText(text []byte) error {
	return json.Unmarshal(text, (*map[string]string)(m))
}

func TestMapWithCustomUnmarshaler(t *testing.T) {
	a := assert.New(t)

	type cfg struct {
		Map customMap `env:"MAP"`
	}
	os.Setenv("MAP", `{"key": "value"}`)
	defer os.Unsetenv("MAP")

	var c cfg
	err := Load(&c, "")
	a.NoError(err)
	a.Equal(customMap{"key": "value"}, c.Map)
}

func TestFileMode(t *testing.T) {
	a := assert.New(t)

	samples := map[string]string{
		"0644": "-rw-r--r--",
		"0777": "-rwxrwxrwx",
	}

	type cfg struct {
		Mode os.FileMode `env:"FILE_MODE"`
	}
	for k, v := range samples {
		os.Setenv("FILE_MODE", k)
		var c cfg

		err := Load(&c, "")
		a.NoError(err)
		a.Equal(v, c.Mode.String())

		os.Unsetenv("FILE_MODE")
	}
}
