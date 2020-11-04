package env

import (
	"fmt"
	"os"
	"strconv"
)

// Example type that represents speed (either in kph, mph or knots).
type speed float64

func (s *speed) UnmarshalText(text []byte) error {
	var speedMultiplier = map[string]float64{
		"kph": 1,
		"mph": 1.60934,
		"kts": 1.852,
	}
	if s == nil {
		return fmt.Errorf("dst is nil pointer")
	}
	str := string(text)
	if len(str) < 4 {
		return fmt.Errorf("input string is too short (%d)", len(str))
	}
	suffix := str[len(str)-3:]
	prefix := str[:len(str)-3]
	val, err := strconv.ParseFloat(prefix, 64)
	if err != nil {
		return fmt.Errorf("not a valid float number %s", prefix)
	}
	mulp, ok := speedMultiplier[suffix]
	if !ok {
		return fmt.Errorf("unrecognized unit %s", suffix)
	}
	*s = speed(val * mulp)
	return nil
}

func ExampleLoad_textUnmarshaller() {
	// These variables will come from the environment.
	os.Setenv("EXAMPLE_SPEED", "40mph")

	type config struct {
		// speed is encoding.TextUnmarshaler.
		Speed speed `env:"SPEED"`
	}

	var c config
	if err := Load(&c, "EXAMPLE_"); err != nil {
		panic(err)
	}
	fmt.Println(c.Speed)
	// Output: 64.3736
}
