package convert

import (
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/invoraapp/invora-controller/gen/invora/billing/common/v2"
	webhookspb "github.com/invoraapp/invora-controller/gen/invora/billing/webhooks/v2"
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

func billingEnum[T ~int32](valueMap map[string]int32, prefix, s string) T {
	key := prefix + strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	if v, ok := valueMap[key]; ok {
		return T(v)
	}
	return 0
}

// CouponType maps CR couponType (e.g. fixed_amount) to proto enum.
func CouponType(s string) commonpb.CouponTypeEnum {
	return billingEnum[commonpb.CouponTypeEnum](commonpb.CouponTypeEnum_value, "COUPON_TYPE_ENUM_", s)
}

// CouponExpiration maps CR expiration (e.g. no_expiration) to proto enum.
func CouponExpiration(s string) commonpb.CouponExpiration {
	return billingEnum[commonpb.CouponExpiration](commonpb.CouponExpiration_value, "COUPON_EXPIRATION_", s)
}

// CouponFrequency maps CR frequency (e.g. forever) to proto enum.
func CouponFrequency(s string) commonpb.CouponFrequency {
	return billingEnum[commonpb.CouponFrequency](commonpb.CouponFrequency_value, "COUPON_FREQUENCY_", s)
}

// BillingTime maps CR billingTime (calendar / anniversary) to proto enum.
func BillingTime(s string) commonpb.BillingTimeEnum {
	return billingEnum[commonpb.BillingTimeEnum](commonpb.BillingTimeEnum_value, "BILLING_TIME_ENUM_", s)
}

// AggregationType maps CR aggregationType to proto enum.
func AggregationType(s string) commonpb.AggregationTypeEnum {
	return billingEnum[commonpb.AggregationTypeEnum](commonpb.AggregationTypeEnum_value, "AGGREGATION_TYPE_ENUM_", s)
}

// WeightedInterval maps CR weightedInterval to proto enum.
func WeightedInterval(s string) commonpb.WeightedIntervalEnum {
	if s == "" {
		return commonpb.WeightedIntervalEnum_WEIGHTED_INTERVAL_ENUM_UNSPECIFIED
	}
	return billingEnum[commonpb.WeightedIntervalEnum](commonpb.WeightedIntervalEnum_value, "WEIGHTED_INTERVAL_ENUM_", s)
}

// WebhookSignatureAlgo maps CR signatureAlgo (hmac / jwt) to proto enum.
func WebhookSignatureAlgo(s string) webhookspb.WebhookEndpointSignatureAlgoEnum {
	if s == "" {
		return webhookspb.WebhookEndpointSignatureAlgoEnum_WEBHOOK_ENDPOINT_SIGNATURE_ALGO_ENUM_UNSPECIFIED
	}
	return billingEnum[webhookspb.WebhookEndpointSignatureAlgoEnum](
		webhookspb.WebhookEndpointSignatureAlgoEnum_value,
		"WEBHOOK_ENDPOINT_SIGNATURE_ALGO_ENUM_",
		s,
	)
}

// TaxRate parses a decimal rate string for billing tax APIs.
func TaxRate(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// PercentageRate parses a percentage rate string for coupons.
func PercentageRate(s string) (float64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Timestamp parses RFC3339 into a protobuf timestamp; returns nil when empty or invalid.
func Timestamp(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return timestamppb.New(t)
}

// MetadataInputs converts a string map to billing metadata inputs.
func MetadataInputs(m map[string]string) []*commonpb.MetadataInput {
	if len(m) == 0 {
		return nil
	}
	out := make([]*commonpb.MetadataInput, 0, len(m))
	for k, v := range m {
		key, val := k, v
		out = append(out, &commonpb.MetadataInput{Key: key, Value: &val})
	}
	return out
}
