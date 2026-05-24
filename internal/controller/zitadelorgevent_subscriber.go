// Package controller — Phase 4 Zitadel event subscriber.
//
// This file implements a manager.Runnable that polls the Zitadel Admin API
// `ListEvents` RPC on an interval, materialises tenant InvoraBillingOrganization CRs
// for `org.added` events, propagates renames via `org.changed`, and removes
// CRs on `org.removed`. Last-processed event sequence is persisted to a
// ConfigMap so restarts do not double-process.
//
// The subscriber retires the backend `billingZitadelSyncHandler.HandleEventAsync`
// path. See plan: phase-4-billing-controller-zitadel-subscriber.md.
package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/admin"
	"github.com/zitadel/zitadel-go/v3/pkg/client/middleware"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	adminpb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	zoidc "github.com/zitadel/oidc/v3/pkg/oidc"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	billingv1alpha1 "github.com/invoraapp/invora-controller/api/v1alpha1"
)

const (
	// SubscriberStateConfigMapName is the ConfigMap holding the last-processed
	// event sequence per Zitadel instance. Lives in the controller's own
	// namespace so it's not deleted by tenant cleanup.
	SubscriberStateConfigMapName = "zitadel-subscriber-state"

	// SubscriberStateConfigMapKey is the data key inside the ConfigMap that
	// stores the last-processed sequence as a decimal string.
	SubscriberStateConfigMapKey = "sequence"

	// defaultSubscriberInterval is how often the subscriber polls Zitadel for
	// new events when no rate is configured. Chosen to balance freshness
	// against load.
	defaultSubscriberInterval = 30 * time.Second

	// defaultSubscriberBatch is the page size for ListEvents calls.
	defaultSubscriberBatch = 100

	// orgAggregateType is the Zitadel event-store aggregate filter for org events.
	orgAggregateType = "org"

	eventTypeOrgAdded   = "org.added"
	eventTypeOrgChanged = "org.changed"
	eventTypeOrgRemoved = "org.removed"
)

// ZitadelOrgEventSubscriberConfig configures a ZitadelOrgEventSubscriber.
type ZitadelOrgEventSubscriberConfig struct {
	// ZitadelDomain is the Zitadel host (e.g. "dev-auth.invora.app").
	ZitadelDomain string

	// ZitadelPort defaults to "443" when empty.
	ZitadelPort string

	// ZitadelInsecure disables TLS verification (HTTP).
	ZitadelInsecure bool

	// SystemAPIUser is the system user name (e.g. "iam_ci").
	SystemAPIUser string

	// SystemAPIPrivateKey is PEM-encoded RSA private key bytes for signing
	// System API JWT bearers.
	SystemAPIPrivateKey []byte

	// AccessToken is a static bearer token. Either this OR a SystemAPI key
	// must be set.
	AccessToken string

	// InvoraBillingInstanceName is the name of the InvoraBillingInstance the subscriber writes
	// against. Tenant InvoraBillingOrganization CRs reference this instance.
	InvoraBillingInstanceName string

	// InvoraBillingInstanceNamespace is the namespace of the InvoraBillingInstance.
	InvoraBillingInstanceNamespace string

	// TenantNamespace is the namespace to create tenant InvoraBillingOrganization CRs
	// in. Typically "invora-dev" / "invora-stg" / "invora-prod".
	TenantNamespace string

	// StateNamespace is the namespace where the state ConfigMap lives.
	// Defaults to the controller's own namespace (POD_NAMESPACE env).
	StateNamespace string

	// Interval is the polling interval. Defaults to 30s.
	Interval time.Duration

	// Batch is the max number of events fetched per ListEvents call.
	Batch uint32
}

// ZitadelOrgEventSubscriber polls a Zitadel admin API and materialises
// `org.*` events into InvoraBillingOrganization CRs. It implements manager.Runnable
// so the controller-runtime manager owns its lifecycle.
type ZitadelOrgEventSubscriber struct {
	client.Client
	Config ZitadelOrgEventSubscriberConfig

	// admin is the connected Zitadel Admin API client, initialised on Start.
	admin *admin.Client
}

// NeedLeaderElection ensures only the leader pod polls Zitadel. Without this
// scale-out replicas would each duplicate event processing.
func (s *ZitadelOrgEventSubscriber) NeedLeaderElection() bool { return true }

// Start is the manager.Runnable entrypoint.
func (s *ZitadelOrgEventSubscriber) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("zitadel-org-event-subscriber")

	if err := s.validateConfig(); err != nil {
		return fmt.Errorf("zitadel subscriber: invalid config: %w", err)
	}

	interval := s.Config.Interval
	if interval <= 0 {
		interval = defaultSubscriberInterval
	}

	adm, err := s.buildAdminClient(ctx)
	if err != nil {
		return fmt.Errorf("zitadel subscriber: building admin client: %w", err)
	}
	s.admin = adm
	defer func() {
		if s.admin != nil && s.admin.Connection != nil {
			_ = s.admin.Connection.Close()
		}
	}()

	logger.Info("zitadel event subscriber started",
		"domain", s.Config.ZitadelDomain,
		"billingInstance", s.Config.InvoraBillingInstanceNamespace+"/"+s.Config.InvoraBillingInstanceName,
		"tenantNamespace", s.Config.TenantNamespace,
		"interval", interval,
	)

	// Run an immediate pass before sleeping so resumed pods catch up fast.
	if err := s.poll(ctx); err != nil {
		logger.Error(err, "initial poll failed; will retry on next tick")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.poll(ctx); err != nil {
				logger.Error(err, "poll failed; will retry on next tick")
			}
		}
	}
}

// validateConfig checks required fields. Missing-but-recoverable fields
// (StateNamespace, Interval, Batch) are filled with defaults in Start.
func (s *ZitadelOrgEventSubscriber) validateConfig() error {
	if s.Config.ZitadelDomain == "" {
		return errors.New("ZitadelDomain is required")
	}
	if s.Config.InvoraBillingInstanceName == "" {
		return errors.New("InvoraBillingInstanceName is required")
	}
	if s.Config.InvoraBillingInstanceNamespace == "" {
		return errors.New("InvoraBillingInstanceNamespace is required")
	}
	if s.Config.TenantNamespace == "" {
		return errors.New("TenantNamespace is required")
	}
	if s.Config.AccessToken == "" && (s.Config.SystemAPIUser == "" || len(s.Config.SystemAPIPrivateKey) == 0) {
		return errors.New("either AccessToken or (SystemAPIUser + SystemAPIPrivateKey) is required")
	}
	return nil
}

// buildAdminClient constructs an authenticated Zitadel Admin API client.
// Mirrors the auth shape used by zitadel-controller's zitadelclient package
// but stays inline so billing-controller doesn't take on that whole package.
func (s *ZitadelOrgEventSubscriber) buildAdminClient(ctx context.Context) (*admin.Client, error) {
	port := s.Config.ZitadelPort
	if port == "" {
		if s.Config.ZitadelInsecure {
			port = "80"
		} else {
			port = "443"
		}
	}
	domain := s.Config.ZitadelDomain + ":" + port
	scheme := "https://"
	if s.Config.ZitadelInsecure {
		scheme = "http://"
	}
	issuer := scheme + s.Config.ZitadelDomain

	opts := []zitadel.Option{}
	if s.Config.ZitadelInsecure {
		opts = append(opts, zitadel.WithInsecure())
	}

	switch {
	case s.Config.AccessToken != "":
		opts = append(opts, zitadel.WithTokenSource(oauth2.StaticTokenSource(&oauth2.Token{
			AccessToken: s.Config.AccessToken,
			TokenType:   string(zoidc.BearerToken),
		})))
	default:
		// System API path — self-signed RS256 JWT against the issuer.
		ts, err := newSystemAPITokenSource(s.Config.SystemAPIPrivateKey, s.Config.SystemAPIUser, issuer)
		if err != nil {
			return nil, fmt.Errorf("creating system API token source: %w", err)
		}
		opts = append(opts, zitadel.WithTokenSource(ts))
	}

	scopes := []string{zoidc.ScopeOpenID, zitadel.ScopeZitadelAPI()}
	_ = middleware.SetOrgID // keep import; placeholder for future per-org scoping

	return admin.NewClient(ctx, issuer, domain, scopes, opts...)
}

// poll fetches the next batch of org events and applies them. Returns
// errors only when state cannot advance — transient transport errors are
// logged and the next tick retries.
func (s *ZitadelOrgEventSubscriber) poll(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("zitadel-org-event-subscriber")

	lastSeq, err := s.readState(ctx)
	if err != nil {
		return fmt.Errorf("reading subscriber state: %w", err)
	}

	batch := s.Config.Batch
	if batch == 0 {
		batch = defaultSubscriberBatch
	}

	req := &adminpb.ListEventsRequest{
		Sequence:       lastSeq,
		Limit:          batch,
		Asc:            true,
		EventTypes:     []string{eventTypeOrgAdded, eventTypeOrgChanged, eventTypeOrgRemoved},
		AggregateTypes: []string{orgAggregateType},
	}
	resp, err := s.admin.ListEvents(ctx, req)
	if err != nil {
		return fmt.Errorf("ListEvents: %w", err)
	}

	highest := lastSeq
	for _, ev := range resp.GetEvents() {
		seq := ev.GetSequence()
		if seq <= lastSeq {
			// ListEvents with sequence is exclusive on most servers, but be
			// defensive — skip anything we've already seen.
			continue
		}
		evType := ev.GetType().GetType()
		aggID := ev.GetAggregate().GetId()
		if aggID == "" {
			logger.V(1).Info("event with empty aggregate id; skipping", "type", evType, "sequence", seq)
			if seq > highest {
				highest = seq
			}
			continue
		}

		switch evType {
		case eventTypeOrgAdded:
			if err := s.handleOrgAdded(ctx, aggID, ev.GetPayload().AsMap()); err != nil {
				logger.Error(err, "handling org.added", "aggregateId", aggID, "sequence", seq)
				// Bail out so we re-attempt this event on the next tick.
				// Persist state up to highest-1 to avoid replay churn.
				return s.writeStateIfAdvanced(ctx, lastSeq, highest)
			}
		case eventTypeOrgChanged:
			if err := s.handleOrgChanged(ctx, aggID, ev.GetPayload().AsMap()); err != nil {
				logger.Error(err, "handling org.changed", "aggregateId", aggID, "sequence", seq)
				return s.writeStateIfAdvanced(ctx, lastSeq, highest)
			}
		case eventTypeOrgRemoved:
			if err := s.handleOrgRemoved(ctx, aggID); err != nil {
				logger.Error(err, "handling org.removed", "aggregateId", aggID, "sequence", seq)
				return s.writeStateIfAdvanced(ctx, lastSeq, highest)
			}
		default:
			logger.V(1).Info("unhandled event type", "type", evType, "sequence", seq)
		}

		if seq > highest {
			highest = seq
		}
	}

	return s.writeStateIfAdvanced(ctx, lastSeq, highest)
}

// handleOrgAdded creates a tenant InvoraBillingOrganization CR for the new org. The
// CR name is `tenant-<zitadel-org-id>` to guarantee uniqueness and let
// re-deliveries be no-ops via apierrors.IsAlreadyExists.
func (s *ZitadelOrgEventSubscriber) handleOrgAdded(ctx context.Context, orgID string, payload map[string]any) error {
	name := tenantOrgCRName(orgID)
	logger := log.FromContext(ctx).WithName("zitadel-org-event-subscriber").WithValues("orgId", orgID, "name", name)

	displayName := stringFromPayload(payload, "name")
	if displayName == "" {
		displayName = name
	}

	desired := &billingv1alpha1.InvoraBillingOrganization{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.Config.TenantNamespace,
			Labels: map[string]string{
				"billing.invora.app/managed-by":  "zitadel-event-subscriber",
				"zitadel.bdaya-dev.com/org-id":   orgID,
				"app.kubernetes.io/component":    "tenant-billing",
			},
		},
		Spec: billingv1alpha1.InvoraBillingOrganizationSpec{
			InstanceRef: billingv1alpha1.ResourceRef{
				Name:      s.Config.InvoraBillingInstanceName,
				Namespace: s.Config.InvoraBillingInstanceNamespace,
			},
			Name:       displayName,
			ExternalID: orgID,
			WriteSecretToRef: billingv1alpha1.WriteSecretToRef{
				Name:      "billing-org-" + orgID + "-api-key",
				Namespace: s.Config.TenantNamespace,
			},
		},
	}

	existing := &billingv1alpha1.InvoraBillingOrganization{}
	err := s.Get(ctx, types.NamespacedName{Name: name, Namespace: s.Config.TenantNamespace}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := s.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Re-delivery race; treat as success.
				return nil
			}
			return fmt.Errorf("creating InvoraBillingOrganization: %w", err)
		}
		logger.Info("materialised tenant InvoraBillingOrganization")
	case err != nil:
		return fmt.Errorf("getting InvoraBillingOrganization: %w", err)
	default:
		// Already exists — keep spec drift handling minimal: only update name
		// to track display rename if changed.
		if existing.Spec.Name != displayName && displayName != "" {
			existing.Spec.Name = displayName
			if err := s.Update(ctx, existing); err != nil {
				return fmt.Errorf("updating InvoraBillingOrganization name: %w", err)
			}
			logger.V(1).Info("updated tenant InvoraBillingOrganization spec.name from org.added", "name", displayName)
		}
	}
	return nil
}

func (s *ZitadelOrgEventSubscriber) handleOrgChanged(ctx context.Context, orgID string, payload map[string]any) error {
	displayName := stringFromPayload(payload, "name")
	if displayName == "" {
		// org.changed events may include other fields; no name change → no-op.
		return nil
	}
	name := tenantOrgCRName(orgID)
	existing := &billingv1alpha1.InvoraBillingOrganization{}
	err := s.Get(ctx, types.NamespacedName{Name: name, Namespace: s.Config.TenantNamespace}, existing)
	if apierrors.IsNotFound(err) {
		// Tenant CR may not exist yet — earlier events may still be queued.
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting InvoraBillingOrganization: %w", err)
	}
	if existing.Spec.Name == displayName {
		return nil
	}
	existing.Spec.Name = displayName
	if err := s.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating InvoraBillingOrganization name: %w", err)
	}
	return nil
}

func (s *ZitadelOrgEventSubscriber) handleOrgRemoved(ctx context.Context, orgID string) error {
	name := tenantOrgCRName(orgID)
	existing := &billingv1alpha1.InvoraBillingOrganization{}
	err := s.Get(ctx, types.NamespacedName{Name: name, Namespace: s.Config.TenantNamespace}, existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting InvoraBillingOrganization: %w", err)
	}
	if err := s.Delete(ctx, existing); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting InvoraBillingOrganization: %w", err)
	}
	return nil
}

// readState loads the last-processed event sequence from the state ConfigMap.
// Returns 0 when no state exists (first run).
func (s *ZitadelOrgEventSubscriber) readState(ctx context.Context) (uint64, error) {
	cm := &corev1.ConfigMap{}
	err := s.Get(ctx, types.NamespacedName{
		Name:      SubscriberStateConfigMapName,
		Namespace: s.stateNamespace(),
	}, cm)
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	raw, ok := cm.Data[SubscriberStateConfigMapKey]
	if !ok || raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing state sequence %q: %w", raw, err)
	}
	return seq, nil
}

// writeStateIfAdvanced persists the new sequence iff it advanced. No-op when
// equal to prior to avoid touching the API server.
func (s *ZitadelOrgEventSubscriber) writeStateIfAdvanced(ctx context.Context, prev, next uint64) error {
	if next <= prev {
		return nil
	}
	return s.writeState(ctx, next)
}

// writeState upserts the state ConfigMap with the given sequence.
func (s *ZitadelOrgEventSubscriber) writeState(ctx context.Context, seq uint64) error {
	ns := s.stateNamespace()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SubscriberStateConfigMapName,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "invora-billing-controller",
				"app.kubernetes.io/component": "zitadel-event-subscriber",
			},
		},
		Data: map[string]string{
			SubscriberStateConfigMapKey: strconv.FormatUint(seq, 10),
		},
	}
	existing := &corev1.ConfigMap{}
	err := s.Get(ctx, types.NamespacedName{Name: cm.Name, Namespace: ns}, existing)
	if apierrors.IsNotFound(err) {
		if err := s.Create(ctx, cm); err != nil {
			return fmt.Errorf("creating state ConfigMap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting state ConfigMap: %w", err)
	}
	if existing.Data == nil {
		existing.Data = make(map[string]string)
	}
	existing.Data[SubscriberStateConfigMapKey] = strconv.FormatUint(seq, 10)
	if err := s.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating state ConfigMap: %w", err)
	}
	return nil
}

func (s *ZitadelOrgEventSubscriber) stateNamespace() string {
	if s.Config.StateNamespace != "" {
		return s.Config.StateNamespace
	}
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	// Fall back to the billing-controller namespace — the controller's own SA
	// always has access here.
	return "invora-billing-controller"
}

// SetupWithManager wires the subscriber into the controller-runtime manager.
// Only registers when Config has a domain set so misconfigured clusters
// silently skip rather than crash on Start.
func (s *ZitadelOrgEventSubscriber) SetupWithManager(mgr ctrl.Manager) error {
	if s.Config.ZitadelDomain == "" {
		return nil
	}
	return mgr.Add(s)
}

// tenantOrgCRName converts a Zitadel org ID into the stable CR name used by
// the subscriber. Kept short and DNS-1123 safe.
func tenantOrgCRName(orgID string) string {
	return "tenant-" + strings.ToLower(orgID)
}

// stringFromPayload reads a string field from a Zitadel event payload map,
// returning "" when missing or non-string.
func stringFromPayload(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	v, ok := payload[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}
