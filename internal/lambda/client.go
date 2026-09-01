// Package lambda is a small client for the Lambda Cloud REST API
// (https://cloud.lambda.ai/api/v1, spec: https://cloud.lambda.ai/api/v1/openapi.json).
package lambda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const DefaultBaseURL = "https://cloud.lambda.ai/api/v1"

// Error codes returned in APIError.Code that callers branch on.
const (
	CodeInsufficientCapacity = "instance-operations/launch/insufficient-capacity"
	CodeInvalidAPIKey        = "global/invalid-api-key"
	CodeObjectDoesNotExist   = "global/object-does-not-exist"
)

// Instance statuses.
const (
	StatusBooting     = "booting"
	StatusActive      = "active"
	StatusUnhealthy   = "unhealthy"
	StatusTerminating = "terminating"
	StatusTerminated  = "terminated"
	StatusPreempted   = "preempted"
)

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func New(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: baseURL, APIKey: apiKey, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// APIError is the error envelope Lambda returns for HTTP >= 400.
type APIError struct {
	Status     int
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("api error %s: %s", e.Code, e.Message)
	if e.Suggestion != "" {
		s += "\n  hint: " + e.Suggestion
	}
	return s
}

// IsCode reports whether err is an *APIError with the given code.
func IsCode(err error, code string) bool {
	if e, ok := err.(*APIError); ok {
		return e.Code == code
	}
	return false
}

type Region struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InstanceTypeSpecs struct {
	VCPUs      int `json:"vcpus"`
	MemoryGiB  int `json:"memory_gib"`
	StorageGiB int `json:"storage_gib"`
	GPUs       int `json:"gpus"`
}

type InstanceType struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	GPUDescription    string            `json:"gpu_description"`
	PriceCentsPerHour int               `json:"price_cents_per_hour"`
	Specs             InstanceTypeSpecs `json:"specs"`
	Architecture      string            `json:"architecture"`
}

func (t InstanceType) PriceUSD() float64 { return float64(t.PriceCentsPerHour) / 100 }

type InstanceTypeItem struct {
	InstanceType                 InstanceType `json:"instance_type"`
	RegionsWithCapacityAvailable []Region     `json:"regions_with_capacity_available"`
}

func (i InstanceTypeItem) HasCapacity(region string) bool {
	for _, r := range i.RegionsWithCapacityAvailable {
		if r.Name == region {
			return true
		}
	}
	return false
}

type Instance struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	IP              string       `json:"ip"`
	PrivateIP       string       `json:"private_ip"`
	Status          string       `json:"status"`
	SSHKeyNames     []string     `json:"ssh_key_names"`
	FileSystemNames []string     `json:"file_system_names"`
	Region          Region       `json:"region"`
	InstanceType    InstanceType `json:"instance_type"`
	Hostname        string       `json:"hostname"`
	JupyterURL      string       `json:"jupyter_url"`
}

type SSHKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
}

type Image struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Family       string `json:"family"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Region       Region `json:"region"`
}

// ImageSpec selects an image by family or by id (exactly one should be set).
type ImageSpec struct {
	ID     string `json:"id,omitempty"`
	Family string `json:"family,omitempty"`
}

type LaunchRequest struct {
	RegionName       string     `json:"region_name"`
	InstanceTypeName string     `json:"instance_type_name"`
	SSHKeyNames      []string   `json:"ssh_key_names"`
	FileSystemNames  []string   `json:"file_system_names,omitempty"`
	Name             string     `json:"name,omitempty"`
	Hostname         string     `json:"hostname,omitempty"`
	Image            *ImageSpec `json:"image,omitempty"`
	UserData         string     `json:"user_data,omitempty"`
}

// do unmarshals the response envelope's "data" field into out.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	raw, err := c.doRaw(ctx, method, path, in)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%s %s: decode envelope: %w", method, path, err)
	}
	return json.Unmarshal(env.Data, out)
}

// doRaw returns the whole response body, for endpoints whose envelope carries
// sibling fields such as page_token.
func (c *Client) doRaw(ctx context.Context, method, path string, in any) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var env struct {
			Error APIError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
			env.Error.Status = resp.StatusCode
			return nil, &env.Error
		}
		return nil, &APIError{Status: resp.StatusCode, Code: fmt.Sprintf("http/%d", resp.StatusCode), Message: string(bytes.TrimSpace(raw))}
	}
	return raw, nil
}

func (c *Client) InstanceTypes(ctx context.Context) (map[string]InstanceTypeItem, error) {
	var out map[string]InstanceTypeItem
	return out, c.do(ctx, http.MethodGet, "/instance-types", nil, &out)
}

func (c *Client) Instances(ctx context.Context) ([]Instance, error) {
	var out []Instance
	return out, c.do(ctx, http.MethodGet, "/instances", nil, &out)
}

func (c *Client) Instance(ctx context.Context, id string) (Instance, error) {
	var out Instance
	return out, c.do(ctx, http.MethodGet, "/instances/"+id, nil, &out)
}

// Launch returns the new instance ids. Rate limit: one call per 12 seconds.
func (c *Client) Launch(ctx context.Context, req LaunchRequest) ([]string, error) {
	var out struct {
		InstanceIDs []string `json:"instance_ids"`
	}
	err := c.do(ctx, http.MethodPost, "/instance-operations/launch", req, &out)
	return out.InstanceIDs, err
}

func (c *Client) Terminate(ctx context.Context, ids []string) ([]Instance, error) {
	var out struct {
		Terminated []Instance `json:"terminated_instances"`
	}
	err := c.do(ctx, http.MethodPost, "/instance-operations/terminate", map[string][]string{"instance_ids": ids}, &out)
	return out.Terminated, err
}

func (c *Client) SSHKeys(ctx context.Context) ([]SSHKey, error) {
	var out []SSHKey
	return out, c.do(ctx, http.MethodGet, "/ssh-keys", nil, &out)
}

func (c *Client) AddSSHKey(ctx context.Context, name, publicKey string) (SSHKey, error) {
	var out SSHKey
	return out, c.do(ctx, http.MethodPost, "/ssh-keys", map[string]string{"name": name, "public_key": publicKey}, &out)
}

func (c *Client) Images(ctx context.Context) ([]Image, error) {
	var out []Image
	return out, c.do(ctx, http.MethodGet, "/images", nil, &out)
}
