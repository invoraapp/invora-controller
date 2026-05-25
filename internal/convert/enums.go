package convert

import (
	"strings"

	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
)

// Currency converts a string currency code (e.g., "SAR") to proto enum.
func Currency(s string) commonpb.CurrencyEnum {
	key := "CURRENCY_ENUM_" + strings.ToUpper(s)
	if v, ok := commonpb.CurrencyEnum_value[key]; ok {
		return commonpb.CurrencyEnum(v)
	}
	return commonpb.CurrencyEnum_CURRENCY_ENUM_UNSPECIFIED
}

// CurrencyString converts a proto enum back to currency code string.
func CurrencyString(e commonpb.CurrencyEnum) string {
	name := e.String()
	return strings.TrimPrefix(name, "CURRENCY_ENUM_")
}

// PlanInterval converts a string interval (e.g., "monthly") to proto enum.
func PlanInterval(s string) commonpb.PlanInterval {
	key := "PLAN_INTERVAL_" + strings.ToUpper(s)
	if v, ok := commonpb.PlanInterval_value[key]; ok {
		return commonpb.PlanInterval(v)
	}
	return commonpb.PlanInterval_PLAN_INTERVAL_UNSPECIFIED
}

// PlanIntervalString converts a proto enum back to interval string.
func PlanIntervalString(e commonpb.PlanInterval) string {
	name := e.String()
	return strings.ToLower(strings.TrimPrefix(name, "PLAN_INTERVAL_"))
}
