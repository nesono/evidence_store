package campaign

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
)

// ExamplePackage expands two small product-side matrices into concrete cases.
// The runtime receives ordinary scenarios: every combination has a stable ref,
// fixed parameters, its own answers and independent attempts/results.
func ExamplePackage(product ProductRevision) (map[string][]byte, error) {
	if err := product.Validate(); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	manifest := Manifest{FormatVersion: 3, Campaign: CampaignInfo{ID: "vehicle-feature-tour", Name: "Vehicle feature tour"}, Source: PackageSource{Repository: product.Repository}, Product: &product}
	add := func(key, title, evidenceType, group, instructions string, parameters map[string]any) error {
		ref := "feature-tour/" + key
		base := "scenarios/" + key + "/"
		scenario := Scenario{Title: title, ProcedureRef: ref, Description: "procedure.md", Parameters: parameters,
			Defaults: Defaults{EvidenceType: evidenceType, Tags: []string{"feature-tour", group}, Metadata: map[string]any{"test_environment": "simulator", "scenario_family": group}},
			Assets:   []string{"setup.png"}, Traceability: []TraceLink{{Kind: "logical_scenario", ID: group, Repository: product.Repository, Path: "docs/testing-campaign.md#" + group}}}
		encoded, err := json.MarshalIndent(scenario, "", "  ")
		if err != nil {
			return err
		}
		files[base+"scenario.yaml"] = append(encoded, '\n') // JSON is a strict YAML subset.
		files[base+"procedure.md"] = []byte("# " + title + "\n\n![Simulator setup](setup.png)\n\n" + strings.ReplaceAll(instructions, "~~~", "```") + "\n")
		manifest.Scenarios = append(manifest.Scenarios, ScenarioEntry{ProcedureRef: ref, Path: base + "scenario.yaml"})
		return nil
	}
	if err := add("readiness", "01 · Simulator readiness and build check", "manual_test", "readiness", `Open the product simulator and record its displayed build. Confirm that it matches the execution's deployed revision. The diagram represents the vehicle, target and observation point used by the following cases.

~~~test-field
id: displayed_build
label: Displayed product build
type: text
required: true
~~~

~~~test-field
id: operator
label: Operator initials
type: text
required: true
~~~

~~~test-field
id: configuration
label: Simulator configuration
type: choice
options: [baseline, instrumented]
default: baseline
required: true
~~~

~~~test-check
id: build_matches
label: Displayed build matches the selected product revision
behavior: expect
~~~

~~~test-check
id: telemetry_ready
label: Telemetry and recording are ready
behavior: expect
~~~`, nil); err != nil {
		return nil, err
	}
	if err := add("operator-demo", "02 · Demonstrate operator display and recording", "demonstration", "operator-demo", `Demonstrate idle → active → paused → idle in the simulator. Start and stop a recording. Attach a screenshot in the test log and optionally link the exported recording below. Each check is independent of the overall demonstration verdict.

~~~test-check
id: state_transitions
label: Display follows all four state transitions
behavior: expect
~~~

~~~test-check
id: recording_playback
label: Exported recording plays back the demonstrated sequence
behavior: expect
~~~

~~~test-field
id: recording_link
label: Recording or external evidence links
type: evidence
~~~

~~~test-field
id: observations
label: Demonstration observations
type: text
multiline: true
~~~`, nil); err != nil {
		return nil, err
	}
	if err := add("sensor-recovery", "03 · Sensor dropout and recovery", "manual_test", "sensor-recovery", `In the simulator, inject a two-second sensor dropout. Observe the degraded-state indicator, restore the sensor and measure recovery time. If the setup cannot inject a dropout, use NOT_EXECUTED with a reason and explain a SKIPPED overall result.

~~~test-check
id: degraded_state
label: Dropout produces a visible degraded-state indication
behavior: expect
~~~

~~~test-field
id: recovery_seconds
label: Measured recovery time (target at most 3 seconds)
type: measurement
unit: s
required: true
~~~

~~~test-check
id: recovered
label: Normal operation returns within 3 seconds
behavior: expect
~~~`, map[string]any{"dropout_seconds": 2, "max_recovery_seconds": 3}); err != nil {
		return nil, err
	}
	for _, speed := range []int{5, 10, 15} {
		for _, load := range []string{"empty", "loaded"} {
			limit := float64(speed) * 0.4
			if load == "loaded" {
				limit += 1
			}
			key := fmt.Sprintf("stopping-%02d-%s", speed, load)
			instructions := fmt.Sprintf(`Select the simulator preset **%s** and command **%d km/h**. Trigger a controlled stop at the marker. Measure distance from the marker to the final position. This case's acceptance limit is **%.1f m**. Record actual speed and distance, then explicitly assess the checks; measurements are not automatically graded.

~~~test-field
id: actual_speed
label: Actual speed before stopping
type: measurement
unit: km/h
required: true
~~~

~~~test-field
id: stopping_distance
label: Measured stopping distance
type: measurement
unit: m
required: true
~~~

~~~test-check
id: stable_stop
label: Stop remains stable with no simulated lane departure
behavior: expect
~~~

~~~test-check
id: within_distance
label: Stopping distance is within %.1f m
behavior: expect
~~~`, load, speed, limit, limit)
			if err := add(key, fmt.Sprintf("Stopping sweep · %d km/h · %s", speed, load), "manual_test", "stopping-sweep", instructions,
				map[string]any{"speed_kph": speed, "load": load, "max_stopping_distance_m": limit}); err != nil {
				return nil, err
			}
		}
	}
	for _, lighting := range []string{"day", "dusk"} {
		for _, target := range []string{"pedestrian", "cone", "vehicle"} {
			instructions := fmt.Sprintf(`Load the simulator's **%s** scene with a **%s** target at **20 m**. Run ten detection frames and record the number detected plus the median latency. The target is at least **9/10 frames** and at most **150 ms**. Attach a representative screenshot to the test log.

~~~test-field
id: detected_frames
label: Detected frames out of ten
type: measurement
unit: frames
required: true
~~~

~~~test-field
id: median_latency
label: Median detection latency
type: measurement
unit: ms
required: true
~~~

~~~test-field
id: classification
label: Observed classification
type: choice
options: [pedestrian, cone, vehicle, unknown]
required: true
~~~

~~~test-check
id: detection_rate
label: Target detected in at least nine of ten frames
behavior: expect
~~~

~~~test-check
id: latency_budget
label: Median latency is at most 150 ms and classification matches the target
behavior: expect
~~~`, lighting, target)
			if err := add("detection-"+lighting+"-"+target, "Detection sweep · "+lighting+" · "+target, "manual_test", "detection-sweep", instructions,
				map[string]any{"lighting": lighting, "target": target, "range_m": 20, "frames": 10, "min_detected_frames": 9, "max_latency_ms": 150}); err != nil {
				return nil, err
			}
		}
	}
	// A real packaged PNG tests asset retention and offline image hydration.
	img := image.NewRGBA(image.Rect(0, 0, 480, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 480; x++ {
			c := color.RGBA{235, 240, 245, 255}
			if y > 60 && y < 120 {
				c = color.RGBA{85, 95, 105, 255}
			}
			if y > 85 && y < 95 && x%50 < 30 {
				c = color.RGBA{255, 255, 255, 255}
			}
			if x > 40 && x < 110 && y > 70 && y < 110 {
				c = color.RGBA{30, 110, 200, 255}
			}
			if x > 350 && x < 370 && y > 65 && y < 115 {
				c = color.RGBA{240, 130, 30, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, img); err != nil {
		return nil, err
	}
	for _, entry := range manifest.Scenarios {
		files[strings.TrimSuffix(entry.Path, "scenario.yaml")+"setup.png"] = picture.Bytes()
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	files["manifest.yaml"] = append(encoded, '\n')
	return files, nil
}
