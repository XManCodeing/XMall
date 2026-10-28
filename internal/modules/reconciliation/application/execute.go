package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/XManCodeing/XMall/internal/constants"
	"github.com/XManCodeing/XMall/internal/logger"
	reconciliationcontract "github.com/XManCodeing/XMall/internal/modules/reconciliation/contract"
	reconciliationdomain "github.com/XManCodeing/XMall/internal/modules/reconciliation/domain"
	"github.com/XManCodeing/XMall/internal/shared/money"

	"github.com/shopspring/decimal"
)

func (s *Service) execute(ctx context.Context, job *reconciliationdomain.Job) ([]reconciliationdomain.Item, error) {
	orders, err := s.procurements.ListByConnectionAndTimeRange(job.ConnectionID, job.TimeRangeStart, job.TimeRangeEnd)
	if err != nil {
		return nil, fmt.Errorf("list procurement orders: %w", err)
	}
	var current reconciliationcontract.UpstreamOrderReader
	mismatches := make([]reconciliationdomain.Item, 0)
	skippedCount, errorCount := 0, 0
	for index := range orders {
		order := &orders[index]
		if skipUnsubmittedOrder(order) {
			skippedCount++
			continue
		}
		detail, diagnostic := s.readOrder(ctx, job.ConnectionID, order, &current)
		if diagnostic != "" {
			errorCount++
			mismatches = append(mismatches, uncertainItem(job, order, diagnostic))
			logger.Warnw("reconciliation_get_upstream_order_failed", "job_id", job.ID,
				"procurement_id", order.ID, "diagnostic", diagnostic)
			continue
		}
		if item := compareOrder(job, order, detail); item != nil {
			mismatches = append(mismatches, *item)
		}
	}
	job.TotalCount = len(orders) - skippedCount
	job.MismatchedCount = len(mismatches)
	job.MatchedCount = job.TotalCount - job.MismatchedCount
	job.ResultJSON = marshalResult(map[string]any{
		"total": job.TotalCount, "matched": job.MatchedCount, "mismatched": job.MismatchedCount,
		"skipped": skippedCount, "errors": errorCount,
	})
	return mismatches, nil
}

func skipUnsubmittedOrder(order *reconciliationcontract.ProcurementOrder) bool {
	if order.UpstreamOrderID != 0 || order.UpstreamOrderExternalID != "" {
		return false
	}
	return order.Status == constants.ProcurementStatusPending ||
		order.Status == constants.ProcurementStatusRejected || order.Status == constants.ProcurementStatusCanceled
}

func (s *Service) readOrder(ctx context.Context, connectionID uint, order *reconciliationcontract.ProcurementOrder, current *reconciliationcontract.UpstreamOrderReader) (*reconciliationcontract.UpstreamOrder, string) {
	if order.UpstreamOrderID == 0 && order.UpstreamOrderExternalID == "" {
		return nil, "missing_upstream_reference"
	}
	reader := *current
	var err error
	if order.ConnectionSnapshot != "" {
		frozen, ok := s.upstream.(interface {
			OpenSnapshot(uint, string) (reconciliationcontract.UpstreamOrderReader, error)
		})
		if !ok {
			return nil, "snapshot_unavailable"
		}
		reader, err = frozen.OpenSnapshot(order.ConnectionID, order.ConnectionSnapshot)
		if err != nil || reader == nil {
			return nil, "snapshot_unavailable"
		}
	} else if reader == nil {
		reader, err = s.upstream.Open(connectionID)
		if err != nil || reader == nil {
			return nil, "connection_unavailable"
		}
		*current = reader
	}
	var detail *reconciliationcontract.UpstreamOrder
	if order.UpstreamOrderExternalID != "" {
		external, ok := reader.(interface {
			GetExternal(context.Context, string) (*reconciliationcontract.UpstreamOrder, error)
		})
		if !ok {
			return nil, "external_query_unsupported"
		}
		detail, err = external.GetExternal(ctx, order.UpstreamOrderExternalID)
	} else {
		detail, err = reader.Get(ctx, order.UpstreamOrderID)
	}
	if err != nil || detail == nil {
		return nil, "upstream_query_failed"
	}
	return detail, ""
}

func uncertainItem(job *reconciliationdomain.Job, order *reconciliationcontract.ProcurementOrder, diagnostic string) reconciliationdomain.Item {
	mismatchType := constants.MismatchTypeStatus
	if job.Type == constants.ReconciliationTypeAmount {
		mismatchType = constants.MismatchTypeAmount
	} else if job.Type == constants.ReconciliationTypeFull {
		mismatchType = constants.MismatchTypeBoth
	}
	return reconciliationdomain.Item{
		JobID: job.ID, ProcurementOrderID: order.ID, LocalOrderNo: order.LocalOrderNo,
		UpstreamOrderExternalID: order.UpstreamOrderExternalID, UpstreamOrderNo: order.UpstreamOrderNo,
		LocalStatus: order.Status, UpstreamStatus: "unknown", LocalAmount: order.UpstreamAmount,
		LocalCurrency: order.UpstreamCurrency, MismatchType: mismatchType, Diagnostic: diagnostic,
	}
}

func compareOrder(job *reconciliationdomain.Job, order *reconciliationcontract.ProcurementOrder, detail *reconciliationcontract.UpstreamOrder) *reconciliationdomain.Item {
	checkStatus := job.Type == constants.ReconciliationTypeStatus || job.Type == constants.ReconciliationTypeFull
	checkAmount := job.Type == constants.ReconciliationTypeAmount || job.Type == constants.ReconciliationTypeFull
	statusMismatch := checkStatus && !reconciliationdomain.IsStatusConsistent(order.Status, detail.Status)
	var upstreamAmount money.Amount
	diagnostic := ""
	if checkAmount {
		upstreamAmount, diagnostic = compareAmount(order, detail)
	}
	amountMismatch := diagnostic != ""
	mismatchType := ""
	switch {
	case statusMismatch && amountMismatch:
		mismatchType = constants.MismatchTypeBoth
	case statusMismatch:
		mismatchType = constants.MismatchTypeStatus
	case amountMismatch:
		mismatchType = constants.MismatchTypeAmount
	}
	if mismatchType == "" {
		return nil
	}
	return &reconciliationdomain.Item{
		UpstreamOrderExternalID: order.UpstreamOrderExternalID,
		JobID:                   job.ID, ProcurementOrderID: order.ID,
		LocalOrderNo: order.LocalOrderNo, UpstreamOrderNo: order.UpstreamOrderNo,
		LocalStatus: order.Status, UpstreamStatus: detail.Status,
		LocalAmount: order.UpstreamAmount, UpstreamAmount: upstreamAmount, MismatchType: mismatchType,
		LocalCurrency: order.UpstreamCurrency, UpstreamCurrency: detail.Currency, Diagnostic: diagnostic,
	}
}

func compareAmount(order *reconciliationcontract.ProcurementOrder, detail *reconciliationcontract.UpstreamOrder) (money.Amount, string) {
	value, err := decimal.NewFromString(strings.TrimSpace(detail.Amount))
	if err != nil || value.IsNegative() {
		return money.Amount{}, "invalid_upstream_amount"
	}
	amount := money.FromDecimal(value)
	if order.UpstreamAmount.IsNegative() {
		return amount, "invalid_local_amount"
	}
	localCurrency := strings.TrimSpace(order.UpstreamCurrency)
	upstreamCurrency := strings.TrimSpace(detail.Currency)
	// Legacy adapters omit currency: compare it only when both sides supply it.
	if localCurrency != "" && upstreamCurrency != "" && !strings.EqualFold(localCurrency, upstreamCurrency) {
		return amount, "currency_mismatch"
	}
	if !order.UpstreamAmount.Equal(value) {
		return amount, "amount_mismatch"
	}
	return amount, ""
}

func marshalResult(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
