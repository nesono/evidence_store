package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCampaignImportWithoutBlobs(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", strings.NewReader("archive"))
	r.Header.Set("Content-Type", "application/gzip")
	NewCampaignHandler(nil, nil).Import(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

type brokenCampaignBody struct{}

func (brokenCampaignBody) Read([]byte) (int, error) { return 0, errors.New("interrupted upload") }
func (brokenCampaignBody) Close() error             { return nil }

func TestCampaignInterruptedBody(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns/import", nil)
	r.Body = brokenCampaignBody{}
	if readCampaignBody(w, r) != nil || w.Code != http.StatusBadRequest {
		t.Fatalf("failed upload not rejected: %d", w.Code)
	}
}
