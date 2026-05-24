package billingclient

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResolveSecretValue reads a single key from a Kubernetes Secret.
//
// SECURITY: When ownerNamespace is non-empty, this function REJECTS any
// secretNamespace that does not match. Cross-namespace Secret reads expand
// the blast radius of CRD authorship: a user with permission to create a
// InvoraBillingTapProvider in ns "team-a" can otherwise dereference any
// Secret in any namespace via the controller's cluster-wide secrets RBAC.
// Pass ownerNamespace = the CR's own namespace to enforce the restriction.
// Pass ownerNamespace = "" only for trusted internal callers (e.g. when the
// Secret reference is fully controller-owned, not authored by tenant CRs).
func ResolveSecretValue(
	ctx context.Context,
	k8sClient client.Client,
	secretName, secretNamespace, secretKey, ownerNamespace string,
) (string, error) {
	if ownerNamespace != "" && secretNamespace != ownerNamespace {
		return "", fmt.Errorf(
			"cross-namespace Secret reference rejected: owner is in %q but referenced Secret is in %q (set ownerNamespace='' on a trusted caller to opt out)",
			ownerNamespace, secretNamespace)
	}

	var secret corev1.Secret
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Namespace: secretNamespace,
		Name:      secretName,
	}, &secret); err != nil {
		return "", fmt.Errorf("getting Secret %s/%s: %w", secretNamespace, secretName, err)
	}

	value, ok := secret.Data[secretKey]
	if !ok {
		return "", fmt.Errorf("key %q not found in Secret %s/%s", secretKey, secretNamespace, secretName)
	}

	return string(value), nil
}
