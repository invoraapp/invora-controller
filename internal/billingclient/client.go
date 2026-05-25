package billingclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
)

// Client communicates with the Invora Billing gateway via gRPC-JSON transcoding.
type Client struct {
	gatewayURL string
	token      string
	orgID      string
	http       *http.Client
}

// Config configures a billing Client.
type Config struct {
	GatewayURL string
	Token      string
	OrgID      string
	HTTPClient *http.Client
}

// New builds a Client from Config.
func New(cfg Config) (*Client, error) {
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
	return &Client{
		gatewayURL: cfg.GatewayURL,
		token:      cfg.Token,
		orgID:      cfg.OrgID,
		http:       httpClient,
	}, nil
}

// APIError is returned for any non-2xx response from the billing gateway.
type APIError struct {
	StatusCode int
	Body       []byte
	Err        string
}

func (e *APIError) Error() string {
	if e.Err != "" {
		return fmt.Sprintf("billing api: %d: %s", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("billing api: %d: %s", e.StatusCode, string(e.Body))
}

// IsNotFound reports whether the error is a 404/NOT_FOUND.
func IsNotFound(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

func (c *Client) call(ctx context.Context, service, method string, req, resp any) error {
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

	url := fmt.Sprintf("%s/%s/%s", c.gatewayURL, service, method)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	if c.orgID != "" {
		httpReq.Header.Set("x-invora-org-id", c.orgID)
	}

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

// CheckConnectivity verifies the gateway is reachable.
func (c *Client) CheckConnectivity(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.gatewayURL+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("connectivity check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("gateway returned %d", resp.StatusCode)
	}
	return nil
}

// WithOrgID returns a shallow copy of the client scoped to the given org.
func (c *Client) WithOrgID(orgID string) *Client {
	cpy := *c
	cpy.orgID = orgID
	return &cpy
}

const (
	svcPlan            = "invora.billing.plan.v2.PlanService"
	svcCustomer        = "invora.billing.customer.v2.CustomerService"
	svcSubscription    = "invora.billing.subscription.v2.SubscriptionService"
	svcAddon           = "invora.billing.addon.v2.AddonService"
	svcCoupon          = "invora.billing.coupon.v2.CouponService"
	svcTax             = "invora.billing.tax.v2.TaxService"
	svcBillableMetric  = "invora.billing.billable_metric.v2.BillableMetricService"
	svcFeature         = "invora.billing.feature.v2.FeatureService"
	svcWebhook         = "invora.billing.webhook_endpoint.v2.WebhookEndpointService"
	svcOrganization    = "invora.billing.organization.v2.OrganizationService"
	svcPaymentProvider = "invora.billing.payment_provider.v2.PaymentProviderService"
)

// ---------------------------------------------------------------------------
// Plans
// ---------------------------------------------------------------------------

type Plan struct {
	ID             string          `json:"id,omitempty"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	AmountCents    int64           `json:"amountCents"`
	AmountCurrency string          `json:"amountCurrency"`
	Interval       string          `json:"interval"`
	PayInAdvance   bool            `json:"payInAdvance"`
	TrialPeriod    float64         `json:"trialPeriod,omitempty"`
	Charges        json.RawMessage `json:"charges,omitempty"`
	TaxCodes       []string        `json:"taxCodes,omitempty"`
}

func (c *Client) CreatePlan(ctx context.Context, p Plan) (*Plan, error) {
	var out Plan
	if err := c.call(ctx, svcPlan, "CreatePlan", &p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetPlan(ctx context.Context, code string) (*Plan, error) {
	var out Plan
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcPlan, "GetPlan", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdatePlan(ctx context.Context, code string, p Plan) (*Plan, error) {
	p.Code = code
	var out Plan
	if err := c.call(ctx, svcPlan, "UpdatePlan", &p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeletePlan(ctx context.Context, code string) error {
	req := map[string]string{"code": code}
	return c.call(ctx, svcPlan, "DestroyPlan", req, nil)
}

// ---------------------------------------------------------------------------
// Billable Metrics
// ---------------------------------------------------------------------------

type BillableMetricFilter struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

type BillableMetric struct {
	ID               string                 `json:"id,omitempty"`
	Code             string                 `json:"code"`
	Name             string                 `json:"name"`
	Description      string                 `json:"description,omitempty"`
	AggregationType  string                 `json:"aggregationType"`
	FieldName        string                 `json:"fieldName,omitempty"`
	WeightedInterval string                 `json:"weightedInterval,omitempty"`
	Recurring        bool                   `json:"recurring,omitempty"`
	Filters          []BillableMetricFilter `json:"filters,omitempty"`
}

func (c *Client) CreateBillableMetric(ctx context.Context, m BillableMetric) (*BillableMetric, error) {
	var out BillableMetric
	if err := c.call(ctx, svcBillableMetric, "CreateBillableMetric", &m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetBillableMetric(ctx context.Context, code string) (*BillableMetric, error) {
	var out BillableMetric
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcBillableMetric, "GetBillableMetric", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateBillableMetric(ctx context.Context, code string, m BillableMetric) (*BillableMetric, error) {
	m.Code = code
	var out BillableMetric
	if err := c.call(ctx, svcBillableMetric, "UpdateBillableMetric", &m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteBillableMetric(ctx context.Context, code string) error {
	req := map[string]string{"code": code}
	return c.call(ctx, svcBillableMetric, "DestroyBillableMetric", req, nil)
}

// ---------------------------------------------------------------------------
// Webhook Endpoints
// ---------------------------------------------------------------------------

type WebhookEndpoint struct {
	ID            string `json:"id,omitempty"`
	WebhookURL    string `json:"webhookUrl"`
	SignatureAlgo string `json:"signatureAlgo,omitempty"`
}

func (c *Client) CreateWebhookEndpoint(ctx context.Context, w WebhookEndpoint) (*WebhookEndpoint, error) {
	var out WebhookEndpoint
	if err := c.call(ctx, svcWebhook, "CreateWebhookEndpoint", &w, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetWebhookEndpoint(ctx context.Context, id string) (*WebhookEndpoint, error) {
	var out WebhookEndpoint
	req := map[string]string{"id": id}
	if err := c.call(ctx, svcWebhook, "GetWebhookEndpoint", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateWebhookEndpoint(ctx context.Context, id string, w WebhookEndpoint) (*WebhookEndpoint, error) {
	w.ID = id
	var out WebhookEndpoint
	if err := c.call(ctx, svcWebhook, "UpdateWebhookEndpoint", &w, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteWebhookEndpoint(ctx context.Context, id string) error {
	req := map[string]string{"id": id}
	return c.call(ctx, svcWebhook, "DestroyWebhookEndpoint", req, nil)
}

func (c *Client) ListWebhookEndpoints(ctx context.Context) ([]WebhookEndpoint, error) {
	var out struct {
		Items []WebhookEndpoint `json:"items"`
	}
	if err := c.call(ctx, svcWebhook, "ListWebhookEndpoints", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ---------------------------------------------------------------------------
// Taxes
// ---------------------------------------------------------------------------

type Tax struct {
	ID          string `json:"id,omitempty"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Rate        string `json:"rate"`
	Description string `json:"description,omitempty"`
}

func (c *Client) CreateTax(ctx context.Context, t Tax) (*Tax, error) {
	var out Tax
	if err := c.call(ctx, svcTax, "CreateTax", &t, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetTax(ctx context.Context, code string) (*Tax, error) {
	var out Tax
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcTax, "GetTax", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateTax(ctx context.Context, code string, t Tax) (*Tax, error) {
	t.Code = code
	var out Tax
	if err := c.call(ctx, svcTax, "UpdateTax", &t, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteTax(ctx context.Context, code string) error {
	req := map[string]string{"code": code}
	return c.call(ctx, svcTax, "DestroyTax", req, nil)
}

// ---------------------------------------------------------------------------
// Add-ons
// ---------------------------------------------------------------------------

type AddOn struct {
	ID             string   `json:"id,omitempty"`
	Code           string   `json:"code"`
	Name           string   `json:"name"`
	Description    string   `json:"description,omitempty"`
	AmountCents    int64    `json:"amountCents"`
	AmountCurrency string   `json:"amountCurrency"`
	TaxCodes       []string `json:"taxCodes,omitempty"`
}

func (c *Client) CreateAddOn(ctx context.Context, a AddOn) (*AddOn, error) {
	var out AddOn
	if err := c.call(ctx, svcAddon, "CreateAddon", &a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetAddOn(ctx context.Context, code string) (*AddOn, error) {
	var out AddOn
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcAddon, "GetAddon", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateAddOn(ctx context.Context, code string, a AddOn) (*AddOn, error) {
	a.Code = code
	var out AddOn
	if err := c.call(ctx, svcAddon, "UpdateAddon", &a, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteAddOn(ctx context.Context, code string) error {
	req := map[string]string{"code": code}
	return c.call(ctx, svcAddon, "DestroyAddon", req, nil)
}

// ---------------------------------------------------------------------------
// Coupons
// ---------------------------------------------------------------------------

type Coupon struct {
	ID             string  `json:"id,omitempty"`
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	CouponType     string  `json:"couponType"`
	Frequency      string  `json:"frequency"`
	Expiration     string  `json:"expiration"`
	AmountCents    *int64  `json:"amountCents,omitempty"`
	AmountCurrency *string `json:"amountCurrency,omitempty"`
	PercentageRate *string `json:"percentageRate,omitempty"`
	ExpirationAt   *string `json:"expirationAt,omitempty"`
	Reusable       bool    `json:"reusable,omitempty"`
}

func (c *Client) CreateCoupon(ctx context.Context, cp Coupon) (*Coupon, error) {
	var out Coupon
	if err := c.call(ctx, svcCoupon, "CreateCoupon", &cp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetCoupon(ctx context.Context, code string) (*Coupon, error) {
	var out Coupon
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcCoupon, "GetCoupon", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateCoupon(ctx context.Context, code string, cp Coupon) (*Coupon, error) {
	cp.Code = code
	var out Coupon
	if err := c.call(ctx, svcCoupon, "UpdateCoupon", &cp, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteCoupon(ctx context.Context, code string) error {
	req := map[string]string{"code": code}
	return c.call(ctx, svcCoupon, "DestroyCoupon", req, nil)
}

// ---------------------------------------------------------------------------
// Features
// ---------------------------------------------------------------------------

type Feature struct {
	ID          string            `json:"id,omitempty"`
	Code        string            `json:"code"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func (c *Client) CreateFeature(ctx context.Context, f Feature) (*Feature, error) {
	var out Feature
	if err := c.call(ctx, svcFeature, "CreateFeature", &f, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetFeature(ctx context.Context, code string) (*Feature, error) {
	var out Feature
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcFeature, "GetFeature", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateFeature(ctx context.Context, code string, f Feature) (*Feature, error) {
	f.Code = code
	var out Feature
	if err := c.call(ctx, svcFeature, "UpdateFeature", &f, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteFeature(ctx context.Context, code string) error {
	req := map[string]string{"code": code}
	return c.call(ctx, svcFeature, "DestroyFeature", req, nil)
}

// ---------------------------------------------------------------------------
// Customers
// ---------------------------------------------------------------------------

type Customer struct {
	ID           string   `json:"id,omitempty"`
	ExternalID   string   `json:"externalId"`
	Name         string   `json:"name,omitempty"`
	Email        string   `json:"email,omitempty"`
	Currency     string   `json:"currency,omitempty"`
	AddressLine1 string   `json:"addressLine1,omitempty"`
	AddressLine2 string   `json:"addressLine2,omitempty"`
	City         string   `json:"city,omitempty"`
	Country      string   `json:"country,omitempty"`
	State        string   `json:"state,omitempty"`
	Zipcode      string   `json:"zipcode,omitempty"`
	LegalName    string   `json:"legalName,omitempty"`
	LegalNumber  string   `json:"legalNumber,omitempty"`
	Phone        string   `json:"phone,omitempty"`
	Timezone     string   `json:"timezone,omitempty"`
	TaxCodes     []string `json:"taxCodes,omitempty"`
}

func (c *Client) CreateOrUpdateCustomer(ctx context.Context, cust Customer) (*Customer, error) {
	var out Customer
	if err := c.call(ctx, svcCustomer, "CreateCustomer", &cust, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetCustomer(ctx context.Context, externalID string) (*Customer, error) {
	var out Customer
	req := map[string]string{"externalId": externalID}
	if err := c.call(ctx, svcCustomer, "GetCustomer", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteCustomer(ctx context.Context, externalID string) error {
	req := map[string]string{"externalId": externalID}
	return c.call(ctx, svcCustomer, "DestroyCustomer", req, nil)
}

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

type Subscription struct {
	ID                 string `json:"id,omitempty"`
	ExternalID         string `json:"externalId"`
	ExternalCustomerID string `json:"externalCustomerId"`
	PlanCode           string `json:"planCode"`
	Name               string `json:"name,omitempty"`
	BillingTime        string `json:"billingTime,omitempty"`
	Status             string `json:"status,omitempty"`
}

func (c *Client) CreateSubscription(ctx context.Context, s Subscription) (*Subscription, error) {
	var out Subscription
	if err := c.call(ctx, svcSubscription, "CreateSubscription", &s, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetSubscription(ctx context.Context, externalID string) (*Subscription, error) {
	var out Subscription
	req := map[string]string{"externalId": externalID}
	if err := c.call(ctx, svcSubscription, "GetSubscription", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateSubscription(ctx context.Context, externalID string, s Subscription) (*Subscription, error) {
	s.ExternalID = externalID
	var out Subscription
	if err := c.call(ctx, svcSubscription, "UpdateSubscription", &s, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) TerminateSubscription(ctx context.Context, externalID string) error {
	req := map[string]string{"externalId": externalID}
	return c.call(ctx, svcSubscription, "TerminateSubscription", req, nil)
}

// ---------------------------------------------------------------------------
// Organizations
// ---------------------------------------------------------------------------

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

func (c *Client) CreateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, error) {
	var out Organization
	if err := c.call(ctx, svcOrganization, "CreateOrganization", &input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var out struct {
		Items []Organization `json:"items"`
	}
	if err := c.call(ctx, svcOrganization, "ListOrganizations", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) GetOrganization(ctx context.Context) (*Organization, error) {
	var out Organization
	if err := c.call(ctx, svcOrganization, "GetOrganization", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateOrganization(ctx context.Context, input CreateOrganizationInput) (*Organization, error) {
	var out Organization
	if err := c.call(ctx, svcOrganization, "UpdateOrganization", &input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DestroyOrganization(ctx context.Context, id string) error {
	req := map[string]string{"id": id}
	return c.call(ctx, svcOrganization, "DestroyOrganization", req, nil)
}

type ApiKey struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func (c *Client) GetOrganizationApiKeys(ctx context.Context, orgID string) ([]ApiKey, error) {
	var out struct {
		Items []ApiKey `json:"items"`
	}
	req := map[string]string{"organizationId": orgID}
	if err := c.call(ctx, svcOrganization, "ListApiKeys", req, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (c *Client) RegenerateOrganizationApiKey(ctx context.Context, orgID, keyID string) (string, error) {
	var out ApiKey
	req := map[string]string{"organizationId": orgID, "id": keyID}
	if err := c.call(ctx, svcOrganization, "RotateApiKey", req, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

// ---------------------------------------------------------------------------
// Payment Providers (Tap)
// ---------------------------------------------------------------------------

type TapPaymentProvider struct {
	ID                 string `json:"id,omitempty"`
	Code               string `json:"code"`
	Name               string `json:"name,omitempty"`
	APIKey             string `json:"apiKey,omitempty"`
	SuccessRedirectURL string `json:"successRedirectUrl,omitempty"`
	SaveCardEnabled    bool   `json:"saveCardEnabled,omitempty"`
	Supports3ds        bool   `json:"supports3ds,omitempty"`
}

func (c *Client) CreateTapPaymentProvider(ctx context.Context, p TapPaymentProvider) (*TapPaymentProvider, error) {
	var out TapPaymentProvider
	if err := c.call(ctx, svcPaymentProvider, "CreateTapPaymentProvider", &p, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type UpdateTapPaymentProviderInput struct {
	ID                 string `json:"id,omitempty"`
	Code               string `json:"code,omitempty"`
	Name               string `json:"name,omitempty"`
	APIKey             string `json:"apiKey,omitempty"`
	SuccessRedirectURL string `json:"successRedirectUrl,omitempty"`
	SaveCardEnabled    *bool  `json:"saveCardEnabled,omitempty"`
	Supports3ds        *bool  `json:"supports3ds,omitempty"`
}

type AddTapPaymentProviderInput struct {
	Code               string `json:"code"`
	Name               string `json:"name,omitempty"`
	APIKey             string `json:"apiKey"`
	SuccessRedirectURL string `json:"successRedirectUrl,omitempty"`
	SaveCardEnabled    bool   `json:"saveCardEnabled,omitempty"`
	Supports3ds        bool   `json:"supports3ds,omitempty"`
}

func (c *Client) UpdateTapPaymentProvider(ctx context.Context, input UpdateTapPaymentProviderInput) (*TapPaymentProvider, error) {
	var out TapPaymentProvider
	if err := c.call(ctx, svcPaymentProvider, "UpdateTapPaymentProvider", &input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) AddTapPaymentProvider(ctx context.Context, input AddTapPaymentProviderInput) (*TapPaymentProvider, error) {
	var out TapPaymentProvider
	if err := c.call(ctx, svcPaymentProvider, "CreateTapPaymentProvider", &input, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) FindTapPaymentProviderByCode(ctx context.Context, code string) (*TapPaymentProvider, error) {
	var out TapPaymentProvider
	req := map[string]string{"code": code}
	if err := c.call(ctx, svcPaymentProvider, "GetTapPaymentProvider", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

