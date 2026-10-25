# Operator display check

## Setup

Open the status display.

```test-field
id: station
type: text
label: Operator station
required: true
```

## Acceptance

```test-check
id: status_visible
label: Current system status is visible
behavior: expect
```

```test-field
id: result
type: choice
label: Overall result
bind: execution.result
options: [PASS, FAIL, ERROR, SKIPPED]
required: true
```
