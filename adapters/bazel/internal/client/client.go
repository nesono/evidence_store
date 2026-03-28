package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxBatchSize = 1000

type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
}

type Option func(*Client)

func WithAPIKey(key string) Option {
	return func(c *Client) { c.apiKey = key }
}

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.httpClient.Timeout = d }
}

func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type EvidenceRecord struct {
	Repo         string         `json:"repo"`
	Branch       string         `json:"branch"`
	RCSRef       string         `json:"rcs_ref"`
	ProcedureRef string         `json:"procedure_ref"`
	EvidenceType string         `json:"evidence_type"`
	Source       string         `json:"source"`
	Result       string         `json:"result"`
	FinishedAt   string         `json:"finished_at"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type batchRequest struct {
	Records []EvidenceRecord `json:"records"`
}

type BatchRecordStatus struct {
	Index  int    `json:"index"`
	ID     string `json:"id,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type BatchResponse struct {
	Results []BatchRecordStatus `json:"results"`
}

// PostBatch sends records to the evidence store, chunking into batches of 1000.
func (c *Client) PostBatch(ctx context.Context, records []EvidenceRecord) ([]BatchResponse, error) {
	var responses []BatchResponse

	for i := 0; i < len(records); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(records) {
			end = len(records)
		}
		chunk := records[i:end]

		resp, err := c.postBatchChunk(ctx, chunk)
		if err != nil {
			return responses, fmt.Errorf("batch chunk %d-%d: %w", i, end-1, err)
		}
		responses = append(responses, *resp)
	}

	return responses, nil
}

func (c *Client) postBatchChunk(ctx context.Context, records []EvidenceRecord) (*BatchResponse, error) {
	body, err := json.Marshal(batchRequest{Records: records})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/evidence/batch", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var batchResp BatchResponse
	if err := json.Unmarshal(respBody, &batchResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &batchResp, nil
}
