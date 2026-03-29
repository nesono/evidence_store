package junitxml

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSinglePass(t *testing.T) {
	f, err := os.Open("testdata/single_pass.xml")
	require.NoError(t, err)
	defer f.Close()

	ts, err := Parse(f)
	require.NoError(t, err)
	require.Len(t, ts.Suites, 1)
	assert.Len(t, ts.Suites[0].Cases, 1)
	assert.Nil(t, ts.Suites[0].Cases[0].Failure)

	result, dur := AggregateResult(ts)
	assert.Equal(t, "PASS", result)
	assert.InDelta(t, 0.5, dur, 0.01)
}

func TestParseMixedResults(t *testing.T) {
	f, err := os.Open("testdata/mixed_results.xml")
	require.NoError(t, err)
	defer f.Close()

	ts, err := Parse(f)
	require.NoError(t, err)
	require.Len(t, ts.Suites, 1)
	assert.Len(t, ts.Suites[0].Cases, 4)

	result, _ := AggregateResult(ts)
	assert.Equal(t, "FAIL", result)
}

func TestParseNestedSuites(t *testing.T) {
	f, err := os.Open("testdata/nested_suites.xml")
	require.NoError(t, err)
	defer f.Close()

	ts, err := Parse(f)
	require.NoError(t, err)
	require.Len(t, ts.Suites, 2)

	result, dur := AggregateResult(ts)
	assert.Equal(t, "PASS", result)
	assert.InDelta(t, 1.5, dur, 0.01)
}

func TestParseErrorResult(t *testing.T) {
	f, err := os.Open("testdata/with_error.xml")
	require.NoError(t, err)
	defer f.Close()

	ts, err := Parse(f)
	require.NoError(t, err)

	result, _ := AggregateResult(ts)
	assert.Equal(t, "ERROR", result)
}

func TestParseAllSkipped(t *testing.T) {
	f, err := os.Open("testdata/all_skipped.xml")
	require.NoError(t, err)
	defer f.Close()

	ts, err := Parse(f)
	require.NoError(t, err)

	result, _ := AggregateResult(ts)
	assert.Equal(t, "SKIPPED", result)
}

func TestParseEmptyStub(t *testing.T) {
	f, err := os.Open("testdata/empty_stub.xml")
	require.NoError(t, err)
	defer f.Close()

	ts, err := Parse(f)
	require.NoError(t, err)
	assert.Nil(t, ts, "empty <testsuites> stub should return nil")
}
