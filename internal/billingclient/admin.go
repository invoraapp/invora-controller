package billingclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	svcOrganization = "invora.billing.organization.v2.OrganizationService"
)

// AdminClient performs super-admin billing organization operations via gRPC-JSON
// transcoding. These RPCs are not yet published in buf.build/invora/billing; keep
// this client until invora.billing.organization.v2.OrganizationService is codegen'd.
type AdminClient struct {
	gatewayURL string
	token      string
	http       *http.Client
}

// AdminConfig configures an AdminClient.
type AdminConfig struct {
	GatewayURL string
	Token      string
	HTTPClient *http.Client
}

// NewAdmin builds an AdminClient from AdminConfig.
func NewAdmin(cfg AdminConfig) (*AdminClient, error) {
	if cfg.GatewayURL == "" {
		return nil, errors.New("billingclient: gateway_url is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("billingclient: token is required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &AdminClient{
		gatewayURL: cfg.GatewayURL,
		token:      cfg.Token,
		http:       httpClient,
	}, nil
}

type CreateOrganizationInput struct {
	Name              string `json:"name"`
	Email             string `json:"email,omitempty"`
	Currency          string `json:"currency,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	DocumentNumbering string `json:"documentNumbering,omitempty"`
}

type Organization struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	Email             string `json:"email,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	DocumentNumbering string `json:"documentNumbering,omitempty"`
}

type ApiKey struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func (c *AdminClient) CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, error) {
	var out Organization
	if err := c.call(ctx, "CreateOrganization", &input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *AdminClient) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var out struct {
		Items []Organization `json:"items"`
	}
	if err := c.call(ctx, "ListOrganizations", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *AdminClient) DestroyOrganization(ctx context.Context, id string) error {
	req := map[string]string{"id": id}
	return c.call(ctx, "DestroyOrganization", req, nil)
}

func (c *AdminClient) GetOrganizationApiKeys(ctx context.Context, orgID string) ([]ApiKey, error) {
	var out struct {
		Items []ApiKey `json:"items"`
	}
	req := map[string]string{"organizationId": orgID}
	if err := c.call(ctx, "ListApiKeys", req, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *AdminClient) RegenerateOrganizationApiKey(ctx context.Context, orgID, keyID string) (string, error) {
	var out ApiKey
	req := map[string]string{"organizationId": orgID, "id": keyID}
	if err := c.call(ctx, "RotateApiKey", req, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func (c *AdminClient) call(ctx context.Context, method string, req, resp any) error {
	var bodyReader io.Reader
	if req != nil {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(req); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		bodyReader = buf
	} else {
		bodyReader = bytes.NewReader([]byte("{}"))
	}

	url := fmt.Sprintf("%s/%s/%s", c.gatewayURL, svcOrganization, method)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: httpResp.StatusCode, Body: respBody}
		var parsed struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &parsed) == nil && parsed.Message != "" {
			apiErr.Err = parsed.Message
		}
		return apiErr
	}

	if resp != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, resp); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
