// weather — demonstrates a tool-providing extension that also takes
// host-supplied CONFIG.
//
// Registers ONE tool the LLM can call:
//
//	weather(city: string) -> short fake weather string
//
// And declares two config fields in extension.json (`config`): a secret
// `api_key` and a `units` select. The user fills them in via the
// /extensions dialog (press `c` on the row); terva delivers the resolved
// values in the handshake and on every change. This demo reads them with
// e.Config() and reacts to changes with e.OnConfig.
//
// Build it:
//
//	cd examples/extensions/weather
//	go build -o weather .
//
// Then drop it next to its extension.json under
// $TERVA_HOME/extensions/weather/, or run `terva ext install ./weather`
// from this directory. Open /extensions, press `c` on "weather", set the
// API key + units, save — then ask "what's the weather in Berlin?".
package main

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strings"

	"terva.sh/terva/packages/agent/ext"
)

const weatherSchema = `{
  "type": "object",
  "properties": {
    "city": {
      "type": "string",
      "description": "City name to look up the weather for."
    }
  },
  "required": ["city"]
}`

func main() {
	e := ext.New("weather", "1.0.0")

	// React to config changes. NEVER log secret values — log only whether
	// the key is set, plus non-secret fields.
	e.OnConfig(func(c ext.Config) {
		e.Logf("config updated: api_key %s, units=%s", setState(c.String("api_key")), c.String("units"))
	})

	e.Tool("weather", "Get the current weather for a city.", json.RawMessage(weatherSchema), func(args json.RawMessage) ext.ToolResult {
		var in struct {
			City string `json:"city"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return ext.TextErrorResult("invalid args: " + err.Error())
		}
		city := strings.TrimSpace(in.City)
		if city == "" {
			return ext.TextErrorResult("city is required")
		}

		cfg := e.Config()
		if cfg.String("api_key") == "" {
			// Guide the user to configure rather than failing opaquely.
			return ext.TextErrorResult("weather is not configured — open /extensions, press c on \"weather\", and set the API key")
		}
		units := cfg.String("units")
		if units == "" {
			units = "celsius"
		}
		return ext.TextResult(fakeWeather(city, units))
	})

	if err := e.Run(); err != nil {
		e.Logf("fatal: %v", err)
	}
}

// setState reports "set"/"unset" for a secret without revealing it.
func setState(v string) string {
	if v == "" {
		return "unset"
	}
	return "set"
}

func fakeWeather(city, units string) string {
	sum := sha1.Sum([]byte(strings.ToLower(city)))
	tempC := (int(sum[0])%30 - 5) // -5..24 °C
	conditions := []string{"sunny", "cloudy", "drizzle", "rain", "thunderstorm", "fog", "snow", "windy"}
	cond := conditions[int(sum[1])%len(conditions)]
	temp, unit := tempC, "°C"
	if units == "fahrenheit" {
		temp, unit = tempC*9/5+32, "°F"
	}
	return fmt.Sprintf("%s: %d%s, %s (this is a deterministic fake; the weather extension is a demo)", city, temp, unit, cond)
}
