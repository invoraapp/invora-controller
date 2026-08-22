package controller

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/types/known/wrapperspb"

	kernel "github.com/invoraapp/invora-controller/gen/kernel"
)

// Adopt-by-code: the shared page-walk used by the org-scoped catalog
// reconcilers (tax, billable metric) to find a pre-existing billing record
// with the CR's `code` BEFORE calling Create.
//
// Why this exists. Billing enforces per-organization uniqueness on `code`, so
// a CR declared against an organization that already owns that code can never
// Create: every reconcile returns
//
//	rpc error: code = InvalidArgument desc = Validation errors: {"code":["value_already_exist"]}
//
// and the CR is stuck Synced=False forever, re-attempting the doomed Create on
// each pass. That is exactly what InvoraBillingPlan/free hit during the
// work_items/162 catalog-ownership migration, and it is what the lago-retire
// migration (invora/devops#56, #57) would hit for the three live `vat_15`
// taxes and the nine live invora-* billable metrics, all of which were created
// by the legacy lago-controller against the SAME underlying billing
// organization.
//
// Why not the import-id annotation. `AnnotationImportID` adoption already
// exists on these reconcilers, but it is unusable from a Config-Sync-managed
// manifest: it is consume-once, so Config Sync re-applies it from git while the
// reconciler strips it on every pass — a KNV2005 fight measured at ~57
// updates/min, whose Status().Update loses the resourceVersion race forever, so
// status.externalId never lands. That approach was tried in invora/devops!227
// and REVERTED in !228; the PO decision of 2026-07-06 is that the controller
// adopts by code instead. See config-sync/invora-controller/billing-plans.yaml.
//
// Why the probe FAILS CLOSED. An earlier form of this pattern read
// `if listResp, err := svc.List(...); err == nil { ... }`, silently discarding
// the error and falling through to Create. A transient gateway failure of the
// adoption probe then turned an idempotent adopt into an unconditional create —
// the mechanism behind invora/devops#109, where one webhook CR minted nine live
// endpoint records. scanPagesForCode therefore returns every transport error to
// its caller, and every caller MUST treat an error as "do not Create".

const (
	// adoptPageLimit is the page size requested while walking a List RPC
	// looking for a record to adopt. The billing List RPCs are paginated
	// (ListResponse carries next_page_cursor), so a single unpaginated call can
	// silently miss the very record we must adopt and fall through to a Create
	// that then fails value_already_exist — the failure this file prevents.
	adoptPageLimit uint64 = 100

	// adoptMaxPages bounds the cursor walk. A server that keeps returning a
	// non-empty next-page cursor must not be able to spin a reconcile forever;
	// exceeding the bound is reported as an error, which (fail-closed) means
	// "do not Create" rather than "create a duplicate".
	adoptMaxPages = 50
)

// adoptPagination builds the PaginationInfo for one page of an adopt probe.
// The first page passes no cursor; subsequent pages carry the opaque cursor
// returned by the previous response.
func adoptPagination(cursor string) *kernel.PaginationInfo {
	p := &kernel.PaginationInfo{Limit: wrapperspb.UInt64(adoptPageLimit)}
	if cursor != "" {
		p.Type = &kernel.PaginationInfo_Cursor{Cursor: cursor}
	}
	return p
}

// scanPagesForCode walks a paginated, org-scoped List RPC until it finds a
// record whose code matches, or the pages are exhausted.
//
// probe fetches exactly one page and reports:
//   - matchID: the external id of a record on that page whose code matches
//     (empty when the page holds no match),
//   - next: the response's next-page cursor ("" when this is the last page),
//   - count: how many items the page held, used to stop on an empty page even
//     if the server still hands back a cursor,
//   - err: any transport/RPC error, which is returned to the caller verbatim.
//
// Returns ("", nil) only when the FULL list was walked without a match — the
// single condition under which a caller may proceed to Create. Any error means
// the probe was inconclusive and the caller must NOT Create.
func scanPagesForCode(
	ctx context.Context,
	code string,
	probe func(ctx context.Context, cursor string) (matchID string, next string, count int, err error),
) (string, error) {
	cursor := ""
	for page := 0; page < adoptMaxPages; page++ {
		matchID, next, count, err := probe(ctx, cursor)
		if err != nil {
			return "", err
		}
		if matchID != "" {
			return matchID, nil
		}
		// Stop on the last page, on an empty page, or on a server that repeats
		// the cursor it was given (which would otherwise loop until the page
		// cap and report a spurious error).
		if next == "" || count == 0 || next == cursor {
			return "", nil
		}
		cursor = next
	}
	return "", fmt.Errorf(
		"adopt-by-code probe for %q did not terminate within %d pages; refusing to Create to avoid a duplicate",
		code, adoptMaxPages)
}
