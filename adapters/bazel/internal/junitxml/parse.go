package junitxml

import (
	"encoding/xml"
	"fmt"
	"io"
)

type TestSuites struct {
	XMLName xml.Name    `xml:"testsuites"`
	Suites  []TestSuite `xml:"testsuite"`
}

type TestSuite struct {
	XMLName   xml.Name   `xml:"testsuite"`
	Name      string     `xml:"name,attr"`
	Tests     int        `xml:"tests,attr"`
	Failures  int        `xml:"failures,attr"`
	Errors    int        `xml:"errors,attr"`
	Skipped   *int       `xml:"skipped,attr"`
	Time      float64    `xml:"time,attr"`
	Timestamp string     `xml:"timestamp,attr"`
	Cases     []TestCase `xml:"testcase"`
}

type TestCase struct {
	Name      string   `xml:"name,attr"`
	ClassName string   `xml:"classname,attr"`
	Time      float64  `xml:"time,attr"`
	Failure   *Failure `xml:"failure"`
	Error     *Error   `xml:"error"`
	Skipped   *Skipped `xml:"skipped"`
}

type Failure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type Error struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type Skipped struct {
	Message string `xml:"message,attr"`
}

// Parse reads JUnit XML from r. Handles both <testsuites> root and bare <testsuite> root.
func Parse(r io.Reader) (*TestSuites, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read xml: %w", err)
	}

	// Try <testsuites> first.
	var suites TestSuites
	if err := xml.Unmarshal(data, &suites); err == nil && len(suites.Suites) > 0 {
		return &suites, nil
	}

	// Fall back to bare <testsuite>.
	var suite TestSuite
	if err := xml.Unmarshal(data, &suite); err != nil {
		return nil, fmt.Errorf("parse xml: %w", err)
	}

	return &TestSuites{Suites: []TestSuite{suite}}, nil
}

// AggregateResult computes the overall result and total duration across all suites.
// Priority: ERROR > FAIL > PASS > SKIPPED.
func AggregateResult(ts *TestSuites) (result string, durationS float64) {
	hasPass := false
	hasFail := false
	hasError := false
	allSkipped := true
	totalCases := 0

	for _, suite := range ts.Suites {
		durationS += suite.Time
		for _, tc := range suite.Cases {
			totalCases++
			switch {
			case tc.Error != nil:
				hasError = true
				allSkipped = false
			case tc.Failure != nil:
				hasFail = true
				allSkipped = false
			case tc.Skipped != nil:
				// remains skipped
			default:
				hasPass = true
				allSkipped = false
			}
		}
	}

	if totalCases == 0 {
		// No test cases — treat as skipped.
		return "SKIPPED", durationS
	}

	switch {
	case hasError:
		return "ERROR", durationS
	case hasFail:
		return "FAIL", durationS
	case allSkipped:
		return "SKIPPED", durationS
	case hasPass:
		return "PASS", durationS
	default:
		return "SKIPPED", durationS
	}
}
