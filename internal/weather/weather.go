// What the weather was doing where a manual test ran.
//
// A manual result made outdoors is made in weather, and the weather is part of
// what it proves: braking distance on a wet surface is a different measurement
// from braking distance on a dry one, and a record that does not say which is a
// record that cannot be compared with the next one. DESIGN.md §2.2 already
// gives the field — `weather_conditions` — and until now it could only be typed
// in from memory, hours later, by someone who was busy running the test.
//
// So the reading is fetched for the place and the hour the record already
// names. It is fetched here rather than in the browser for the same reason the
// map link in a record is never followed until a reader clicks it: a tester
// filling in a form should not have their position handed to a third party by
// the page, and an operator should be able to see, configure or switch off the
// one place this store talks to the outside world.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrNoReading means the service answered and has nothing for that place and
// hour — a date outside the window it keeps, or an hour with no numbers in it.
// It is not a failure of this store or of the service, and nothing will fix it,
// so the tester is told to write the weather in themselves rather than invited
// to try again.
var ErrNoReading = errors.New("no weather reading for that place and time")

// Reading is the weather at one place in one hour.
//
// Every measurement is a pointer because a service that has the sky but not the
// wind should still be able to say so, and a zero that means "not reported"
// would be a claim about a still day.
type Reading struct {
	// ObservedAt is the hour the reading is for, which is not the minute the
	// test finished: hourly is the resolution weather models publish at.
	ObservedAt       time.Time `json:"observed_at"`
	Description      string    `json:"description"`
	TemperatureC     *float64  `json:"temperature_c,omitempty"`
	RelativeHumidity *float64  `json:"relative_humidity,omitempty"`
	PrecipitationMM  *float64  `json:"precipitation_mm,omitempty"`
	WindSpeedKPH     *float64  `json:"wind_speed_kph,omitempty"`
}

// Summary writes the reading as the one line that goes in the field.
//
// The stored value is text, as DESIGN.md has it, because the tester may correct
// it: someone standing in a hailstorm the model put two valleys away needs to
// be able to say so, and a structured record they cannot argue with would file
// the model's account over theirs.
func (r Reading) Summary() string {
	parts := []string{}
	if r.Description != "" {
		parts = append(parts, r.Description)
	}
	if r.TemperatureC != nil {
		parts = append(parts, trimFloat(*r.TemperatureC)+" °C")
	}
	if r.WindSpeedKPH != nil {
		parts = append(parts, "wind "+trimFloat(math.Round(*r.WindSpeedKPH))+" km/h")
	}
	if r.RelativeHumidity != nil {
		parts = append(parts, "humidity "+trimFloat(math.Round(*r.RelativeHumidity))+"%")
	}
	// Rain is worth naming when there is any and worth denying when there is
	// none: "dry" is a fact about a braking test, not the absence of one.
	if r.PrecipitationMM != nil {
		if *r.PrecipitationMM > 0 {
			parts = append(parts, "precipitation "+trimFloat(*r.PrecipitationMM)+" mm")
		} else {
			parts = append(parts, "no precipitation")
		}
	}
	return strings.Join(parts, ", ")
}

// trimFloat prints a measurement without the trailing zeros of a precision the
// service never claimed.
func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// Provider is where a reading comes from. It is an interface so the API handler
// can be tested without the network, and so an operator with their own weather
// service has somewhere to put it.
type Provider interface {
	At(ctx context.Context, lat, lon float64, when time.Time) (Reading, error)
}

// OpenMeteo reads from Open-Meteo, which needs no account and no key. That
// matters more than the choice of service: a feature that only works once
// someone has signed up for something is a feature most deployments never turn
// on.
type OpenMeteo struct {
	endpoint string
	client   *http.Client
}

func NewOpenMeteo(endpoint string, timeout time.Duration) *OpenMeteo {
	return &OpenMeteo{
		endpoint: endpoint,
		client:   &http.Client{Timeout: timeout},
	}
}

// The variables a tester would otherwise write down by hand. Anything more is
// noise in a one-line field.
const hourlyVariables = "temperature_2m,relative_humidity_2m,precipitation,weather_code,wind_speed_10m"

// coordinatePrecision is how much of the point is passed on: two decimals is
// about a kilometre, well under the resolution of any weather model, and enough
// coarser than the record's own five decimals that the service is not told
// which bench in the building the test ran at.
const coordinatePrecision = 2

func (o *OpenMeteo) At(ctx context.Context, lat, lon float64, when time.Time) (Reading, error) {
	// Both sides of the hour, because a test that finished at 13:50 is better
	// described by the 14:00 reading, and asking for one hour would settle that
	// by rounding rather than by which reading is nearer.
	hour := when.UTC().Truncate(time.Hour)

	query := url.Values{
		"latitude":        {strconv.FormatFloat(lat, 'f', coordinatePrecision, 64)},
		"longitude":       {strconv.FormatFloat(lon, 'f', coordinatePrecision, 64)},
		"hourly":          {hourlyVariables},
		"timezone":        {"UTC"},
		"wind_speed_unit": {"kmh"},
		"start_hour":      {hour.Format("2006-01-02T15:04")},
		"end_hour":        {hour.Add(time.Hour).Format("2006-01-02T15:04")},
	}

	endpoint, err := url.Parse(o.endpoint)
	if err != nil {
		return Reading{}, fmt.Errorf("weather endpoint: %w", err)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Reading{}, err
	}

	resp, err := o.client.Do(req)
	if err != nil {
		return Reading{}, fmt.Errorf("weather service unreachable: %w", err)
	}
	defer resp.Body.Close()

	// Capped because this is a response from somewhere else, and an endpoint
	// that has been pointed at the wrong host should not be able to fill this
	// process's memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Reading{}, fmt.Errorf("weather service: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return Reading{}, o.statusError(resp.StatusCode, body)
	}

	var payload struct {
		Hourly struct {
			Time          []string   `json:"time"`
			Temperature   []*float64 `json:"temperature_2m"`
			Humidity      []*float64 `json:"relative_humidity_2m"`
			Precipitation []*float64 `json:"precipitation"`
			WeatherCode   []*float64 `json:"weather_code"`
			WindSpeed     []*float64 `json:"wind_speed_10m"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Reading{}, fmt.Errorf("weather service: unreadable response: %w", err)
	}

	// Nearest first, so an hour with numbers in it is preferred to a nearer one
	// without.
	for _, i := range byDistanceFrom(payload.Hourly.Time, when) {
		reading := Reading{
			TemperatureC:     valueAt(payload.Hourly.Temperature, i),
			RelativeHumidity: valueAt(payload.Hourly.Humidity, i),
			PrecipitationMM:  valueAt(payload.Hourly.Precipitation, i),
			WindSpeedKPH:     valueAt(payload.Hourly.WindSpeed, i),
		}
		if code := valueAt(payload.Hourly.WeatherCode, i); code != nil {
			reading.Description = Describe(int(*code))
		}
		if reading.Description == "" && reading.TemperatureC == nil &&
			reading.RelativeHumidity == nil && reading.PrecipitationMM == nil &&
			reading.WindSpeedKPH == nil {
			continue
		}
		// Parsed rather than recomputed from `when`: the hour in the answer is
		// the hour the service actually read, and saying which one it was is
		// what lets a reader tell a reading from a guess.
		observed, err := time.Parse("2006-01-02T15:04", payload.Hourly.Time[i])
		if err != nil {
			return Reading{}, fmt.Errorf("weather service: unreadable timestamp %q", payload.Hourly.Time[i])
		}
		reading.ObservedAt = observed.UTC()
		return reading, nil
	}

	return Reading{}, ErrNoReading
}

// statusError separates a service that has nothing for this place and hour from
// a service that is broken. The first is the tester's business — they type the
// weather in — and the second is the operator's.
func (o *OpenMeteo) statusError(status int, body []byte) error {
	var payload struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &payload)

	// A 400 here is not a malformed request from a browser: the query is built
	// above. It is the service saying the date is outside the window it keeps,
	// and its own wording says what that window is.
	if status == http.StatusBadRequest {
		if payload.Reason != "" {
			return fmt.Errorf("%w: %s", ErrNoReading, payload.Reason)
		}
		return ErrNoReading
	}

	if payload.Reason != "" {
		return fmt.Errorf("weather service returned %d: %s", status, payload.Reason)
	}
	return fmt.Errorf("weather service returned %d", status)
}

// byDistanceFrom orders the hours in an answer by how near each is to the time
// the test finished.
func byDistanceFrom(times []string, when time.Time) []int {
	order := make([]int, 0, len(times))
	for i := range times {
		order = append(order, i)
	}
	distance := func(i int) time.Duration {
		parsed, err := time.Parse("2006-01-02T15:04", times[i])
		if err != nil {
			// Unreadable hours sort last rather than out: whether the answer is
			// usable is decided by whether it has numbers in it, above.
			return time.Duration(math.MaxInt64)
		}
		return absDuration(parsed.UTC().Sub(when.UTC()))
	}
	// Two hours, sometimes three. An insertion sort keeps the whole ordering in
	// one readable place.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && distance(order[j]) < distance(order[j-1]); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	return order
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// valueAt reads one hour out of a variable's series, tolerating a service that
// sent a shorter series than it sent hours.
func valueAt(series []*float64, i int) *float64 {
	if i >= len(series) {
		return nil
	}
	return series[i]
}

// wmoCodes is the present-weather table WMO code 4677 defines and every service
// that publishes an hourly forecast reports against.
var wmoCodes = map[int]string{
	0:  "Clear sky",
	1:  "Mainly clear",
	2:  "Partly cloudy",
	3:  "Overcast",
	45: "Fog",
	48: "Depositing rime fog",
	51: "Light drizzle",
	53: "Moderate drizzle",
	55: "Dense drizzle",
	56: "Light freezing drizzle",
	57: "Dense freezing drizzle",
	61: "Slight rain",
	63: "Moderate rain",
	65: "Heavy rain",
	66: "Light freezing rain",
	67: "Heavy freezing rain",
	71: "Slight snow",
	73: "Moderate snow",
	75: "Heavy snow",
	77: "Snow grains",
	80: "Slight rain showers",
	81: "Moderate rain showers",
	82: "Violent rain showers",
	85: "Slight snow showers",
	86: "Heavy snow showers",
	95: "Thunderstorm",
	96: "Thunderstorm with slight hail",
	99: "Thunderstorm with heavy hail",
}

// Describe names a present-weather code. A code this table does not have is
// still something the service observed, so it is passed through as itself
// rather than dropped: a record saying "weather code 42" says more than a
// record saying nothing, and it is a number a reader can look up.
func Describe(code int) string {
	if description, ok := wmoCodes[code]; ok {
		return description
	}
	return "weather code " + strconv.Itoa(code)
}
