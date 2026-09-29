package remotebackup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const checkBatchSize = 5000

// APIError is a non-2xx response from the Backupper.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("backupper returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("backupper returned HTTP %d: %s", e.Status, e.Message)
}

// MissingFilesError is the 409 returned when a snapshot references file objects
// the Backupper has not received yet.
type MissingFilesError struct {
	Missing []string
}

func (e *MissingFilesError) Error() string {
	return fmt.Sprintf("backupper is missing %d file(s) referenced by the snapshot", len(e.Missing))
}

// Client talks to one Backupper over its v1 API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *Client) apiURL(path string) string {
	return c.baseURL + "/api/v1" + path
}

func (c *Client) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, c.apiURL(path), body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return req, nil
}

func (c *Client) do(req *http.Request, out any) error {
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the backupper: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusConflict {
		var conflict struct {
			Error   string   `json:"error"`
			Missing []string `json:"missing"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&conflict)
		if len(conflict.Missing) > 0 {
			return &MissingFilesError{Missing: conflict.Missing}
		}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := ""
		var apiErr ErrorResponse
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&apiErr); err == nil {
			msg = apiErr.Error
		}
		return &APIError{Status: res.StatusCode, Message: msg}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<22)).Decode(out); err != nil {
		return fmt.Errorf("cannot decode the backupper response: %w", err)
	}
	return nil
}

func (c *Client) getJSON(path string, out any) error {
	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

func (c *Client) postJSON(path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := c.newRequest(http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) Health() (Health, error) {
	var h Health
	err := c.getJSON("/health", &h)
	return h, err
}

func (c *Client) Capabilities() (Capabilities, error) {
	var cap Capabilities
	err := c.getJSON("/capabilities", &cap)
	return cap, err
}

// Check returns the subset of hashes the Backupper does not have yet. An empty
// result means there is nothing to upload.
func (c *Client) Check(hashes []string) ([]string, error) {
	missing := make([]string, 0)
	for start := 0; start < len(hashes); start += checkBatchSize {
		end := start + checkBatchSize
		if end > len(hashes) {
			end = len(hashes)
		}
		var resp CheckResponse
		if err := c.postJSON("/files/check", CheckRequest{Hashes: hashes[start:end]}, &resp); err != nil {
			return nil, err
		}
		missing = append(missing, resp.Missing...)
	}
	return missing, nil
}

// PutFile uploads one file object. size must be the exact byte count; an empty
// file is uploaded with a zero Content-Length.
func (c *Client) PutFile(hash string, size int64, r io.Reader) (PutFileResponse, error) {
	var out PutFileResponse
	req, err := c.newRequest(http.MethodPut, "/files/"+url.PathEscape(hash), http.NoBody)
	if err != nil {
		return out, err
	}
	if size > 0 {
		req.Body = io.NopCloser(r)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	return out, c.do(req, &out)
}

func (c *Client) OpenFile(hash string) (io.ReadCloser, int64, error) {
	req, err := c.newRequest(http.MethodGet, "/files/"+url.PathEscape(hash), nil)
	if err != nil {
		return nil, 0, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach the backupper: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		return nil, 0, &APIError{Status: res.StatusCode, Message: "cannot download file " + hash}
	}
	return res.Body, res.ContentLength, nil
}

func (c *Client) CommitSnapshot(req CreateSnapshotRequest) (CreateSnapshotResponse, error) {
	var out CreateSnapshotResponse
	err := c.postJSON("/snapshots", req, &out)
	return out, err
}

func (c *Client) ListSnapshots() ([]SnapshotInfo, error) {
	var resp SnapshotListResponse
	if err := c.getJSON("/snapshots", &resp); err != nil {
		return nil, err
	}
	return resp.Snapshots, nil
}

func (c *Client) GetSnapshot(id string) (SnapshotResponse, error) {
	var resp SnapshotResponse
	err := c.getJSON("/snapshots/"+url.PathEscape(id), &resp)
	return resp, err
}

// DeletePreview reports what deleting a snapshot would free, without deleting.
func (c *Client) DeletePreview(id string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.getJSON("/snapshots/"+url.PathEscape(id)+"/delete-preview", &out)
	return out, err
}
