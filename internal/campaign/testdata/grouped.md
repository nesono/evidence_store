# Daily equipment check

## Operator context

```test-field
id: operator
type: text
label: Operator
required: true
```

## Display

```test-check
id: display_ready
label: Display is ready
behavior: expect
```

```test-field
id: display_ready_comment
type: text
label: Display observations
multiline: true
```

```test-field
id: display_ready_issues
type: evidence
label: Related issue links
```

## Recording

```test-check
id: recording_ready
label: Recording can be started
behavior: expect
```

```test-field
id: recording_ready_comment
type: text
label: Recording observations
multiline: true
```
