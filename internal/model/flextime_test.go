package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFlexibleTime(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		{
			name:  "RFC3339 UTC",
			input: "2026-03-30T14:00:00Z",
			want:  time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name:  "RFC3339 with offset",
			input: "2026-03-30T14:00:00+02:00",
			want:  time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC),
		},
		{
			name:  "RFC3339Nano",
			input: "2026-03-30T14:00:00.123456789Z",
			want:  time.Date(2026, 3, 30, 14, 0, 0, 123456789, time.UTC),
		},
		{
			name:  "zoneless T separator",
			input: "2026-03-30T14:00:00",
			want:  time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name:  "zoneless space separator",
			input: "2026-03-30 14:00:00",
			want:  time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name:  "zoneless short (no seconds)",
			input: "2026-03-30 14:00",
			want:  time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name:  "date only",
			input: "2026-03-30",
			want:  time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "whitespace trimmed",
			input: "  2026-03-30 14:00  ",
			want:  time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name:    "invalid format",
			input:   "not-a-date",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFlexibleTime(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, time.UTC, got.Location(), "result must be in UTC")
		})
	}
}

func TestFlexibleTimeUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    time.Time
		wantErr bool
	}{
		{
			name: "RFC3339 UTC",
			json: `{"t":"2026-03-30T14:00:00Z"}`,
			want: time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name: "with offset normalizes to UTC",
			json: `{"t":"2026-03-30T16:00:00+02:00"}`,
			want: time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name: "zoneless treated as UTC",
			json: `{"t":"2026-03-30 14:00"}`,
			want: time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC),
		},
		{
			name: "date only",
			json: `{"t":"2026-03-30"}`,
			want: time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "invalid",
			json:    `{"t":"garbage"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v struct {
				T FlexibleTime `json:"t"`
			}
			err := json.Unmarshal([]byte(tt.json), &v)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, v.T.Time)
		})
	}
}

func TestFlexibleTimeMarshalJSON(t *testing.T) {
	ft := FlexibleTime{Time: time.Date(2026, 3, 30, 14, 0, 0, 0, time.UTC)}
	b, err := json.Marshal(ft)
	require.NoError(t, err)
	assert.Equal(t, `"2026-03-30T14:00:00Z"`, string(b))
}
