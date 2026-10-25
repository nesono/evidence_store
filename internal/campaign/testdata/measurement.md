# Distance measurement

Measure the distance between the marked points using the indicated instrument.

```test-field
id: surface
type: choice
label: Actual surface
options: [dry, wet]
default: dry
```

```test-field
id: distance
type: measurement
label: Measured distance
unit: m
required: true
```

```test-field
id: supporting_evidence
type: evidence
label: Photos and recording links
```

```test-field
id: assessment
type: text
label: Explain the measurement
multiline: true
```
