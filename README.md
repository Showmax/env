# env
`env` is yet another package for parsing various data types from environment
variables. We decided to write this package as none of the avalaible
packages met our needs. The closest one was
[envconfig](https://github.com/kelseyhightower/envconfig) but it has several
drawbacks, for example:

* The environment variables are optional by default. This is not aligned
  with our requirements as we believe that optional variables in deployment
  and environment configuration are bug prone practice. Writing
  `envconfig:required` annotation to every field is very annoying and
  pollutes the code.

* Variable name lookup policy is too complicated and therefore again bug prone.
  If there is variable named `VAR` loaded with prefix `PREFIX` and variable
  `PREFIX_VAR` does not exist in the environment, `envconfig` tries to read
  from variable `VAR` (without the prefix). If such variable exists in the
  environment by accident, completely different value is loaded and no one
  is notified about that.

* On error, only the first one error is reported. This requires to re-run
  the program every time an error is resolved.

## Usage

The usage is really straightforward. The environment variables are
identified using go annotations (`env` prefix followed by variable name). So
you can write your configuration structure like:

```
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
}

type Foo struct {
	Foo string `env:"FOO"`
}

type Bar struct {
	Bar string `env:"BAR"`
}

```

As you can see, even structure embedding and composition is supported so you
can define arbitrarily complex structure. The naming scheme follows the
nesting, so for example the env variable corresponding to `Bar` string field
inside `Bar` struct will have name `BAR_BAR` (besides the global prefix, see
below).

The configuration can be then parsed and loaded like this:

```
package main

import github.com/Showmax/env

func main() {
	var cfg config
	err := env.Load(&cfg, "PREFIX_")
}
```

All env variable names must be prefixed by `PREFIX_` in this example so the
final variable name will not be `BAR_BAR` but `PREFIX_BAR_BAR`. Empty prefix
is also allowed.

## Parsers

The actual parsing is driven by data-type of particular fields in the config
structure. Based on the data-type, concrete parser is chosen. The parser
look-up procedure goes as follows:

1. Default parser map is examined (see default parsers for extensive list).
   This map contains parsers for all primitive data types and also for some
   simple types from go base library (such as `time.Duration` or `url.URL`).
2. If the corresponding parser is not found in the default parsers map, we
   try to cast the type to `TextUnmarshaller` interface and use it for
   parsing the value.

For internal go composite types (such as pointers, slices or maps), we
provide built-in support. See below.

### Following pointers

When the value is a pointer (potentially a pointer to pointer, or pointer to
pointer to another pointer, etc.), we follow the pointer chain and
automatically initialize all intermediate pointers on the path.

### Parsing slices

We also support parsing slices. If a data field is declared as slice, the
corresponding value parsed from env is treated as comma-separated list of
values loaded into the slice This behavior is baked-in and is not
configurable.

The following rules apply to the individual slice items:

* Individual items are parsed recursively using the same rules according to
  their underlying data-type.
* A slice value cannot contain null-byte.
* The individual values may be enclosed in double-quotes (useful when an
  item contains spaces or similar).
* An empty slice item must be always enclosed in double-quotes.
* If, for some reason, a double-quote have to be contained in one of the
  values, it must be prefixed (escaped) by back-slash.

### Parsing maps

Maps are treated in a special way. Map keys are bound to a suffix of the
environment variable name. Let's describe it by an example - suppose the
following code:

```
type config struct {
	Map map[int]string `env:"MAP_"`
}

func main() {
	var c config
	err := env.Load(&cfg, "PREFIX_")
}
```

And the following environment variables exported:

```
$> export PREFIX_MAP_42=foo PREFIX_MAP_84=bar
```

This will produce the following map:

```
map[int]string{
	42: "foo",
	84: "bar",
}
```

Individual map elements (both keys and values) are parsed recursively
according to their underlying data-type.

## Tests and examples

Please see our tests for more detailed examples.
