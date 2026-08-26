package ops

import (
	"context"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
)

// ---------------------------------------------------------------------------
// v_pipeline: the funnel, grouped by area and stage. Read-only aggregate -
// there is deliberately no grand total (005_business_core.sql explains why:
// a contract, a prize and an impression count are not one currency).
// ---------------------------------------------------------------------------

// PipelineSummary is one (area, stage) bucket of the opportunity funnel.
type PipelineSummary struct {
	AreaID              *string `json:"area_id"`
	AreaName            *string `json:"area_name"`
	Stage               string  `json:"stage"`
	IsOpen              bool    `json:"is_open"`
	OpportunityCount    int     `json:"opportunity_count"`
	EstAmountTotal      float64 `json:"est_amount_total"`
	WeightedAmountTotal float64 `json:"weighted_amount_total"`
}

// PipelineFilter narrows BizPipeline.
type PipelineFilter struct {
	Limit int
}

// BizPipeline reads the opportunity funnel from v_pipeline, grouped by area
// and stage.
func (s *Store) BizPipeline(ctx context.Context, f PipelineFilter) ([]PipelineSummary, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT area_id, area_name, stage, is_open, opportunity_count,
               est_amount_total, weighted_amount_total
          FROM v_pipeline
         ORDER BY area_name, stage
         LIMIT ?`, limit)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []PipelineSummary{}
	for rows.Next() {
		var p PipelineSummary
		var isOpen int
		if err := rows.Scan(&p.AreaID, &p.AreaName, &p.Stage, &isOpen, &p.OpportunityCount,
			&p.EstAmountTotal, &p.WeightedAmountTotal); err != nil {
			return nil, sqlite.Classify(err)
		}
		p.IsOpen = isOpen == 1
		out = append(out, p)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// v_contract_receivable: declared vs. planned vs. received per contract, plus
// the mismatch facts. The declared amount is authoritative and is never
// adjusted to agree with the plans or receipts (005_business_core.sql).
// ---------------------------------------------------------------------------

// ContractReceivable is one contract's money picture.
type ContractReceivable struct {
	ContractID        string   `json:"contract_id"`
	ContractNo        *string  `json:"contract_no"`
	Name              string   `json:"name"`
	Kind              string   `json:"kind"`
	Status            string   `json:"status"`
	Currency          string   `json:"currency"`
	AccountID         string   `json:"account_id"`
	AccountName       *string  `json:"account_name"`
	DeclaredAmount    float64  `json:"declared_amount"`
	PlannedAmount     float64  `json:"planned_amount"`
	PlanCount         int      `json:"plan_count"`
	WaivedAmount      float64  `json:"waived_amount"`
	ReceivedAmount    float64  `json:"received_amount"`
	ReceiptCount      int      `json:"receipt_count"`
	LastReceiptAt     *string  `json:"last_receipt_at"`
	OutstandingAmount float64  `json:"outstanding_amount"`
	ReceivedRatio     float64  `json:"received_ratio"`
	PlanGap           float64  `json:"plan_gap"`
	PlanMismatch      bool     `json:"plan_mismatch"`
	OverReceived      bool     `json:"over_received"`
	UnitPrice         *float64 `json:"unit_price"`
	Quantity          *float64 `json:"quantity"`
	LineMismatch      bool     `json:"line_mismatch"`
}

// ReceivableFilter narrows ListReceivables.
type ReceivableFilter struct {
	AccountID string
	Limit     int
}

// ListReceivables reads v_contract_receivable, largest outstanding balance
// first.
func (s *Store) ListReceivables(ctx context.Context, f ReceivableFilter) ([]ContractReceivable, error) {
	query := `
        SELECT contract_id, contract_no, name, kind, status, currency,
               account_id, account_name, declared_amount, planned_amount,
               plan_count, waived_amount, received_amount, receipt_count,
               last_receipt_at, outstanding_amount, received_ratio, plan_gap,
               plan_mismatch, over_received, unit_price, quantity, line_mismatch
          FROM v_contract_receivable WHERE 1=1`
	var args []any
	if f.AccountID != "" {
		query += " AND account_id = ?"
		args = append(args, f.AccountID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY outstanding_amount DESC, contract_id LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []ContractReceivable{}
	for rows.Next() {
		var r ContractReceivable
		var planMismatch, overReceived, lineMismatch int
		if err := rows.Scan(&r.ContractID, &r.ContractNo, &r.Name, &r.Kind, &r.Status, &r.Currency,
			&r.AccountID, &r.AccountName, &r.DeclaredAmount, &r.PlannedAmount,
			&r.PlanCount, &r.WaivedAmount, &r.ReceivedAmount, &r.ReceiptCount,
			&r.LastReceiptAt, &r.OutstandingAmount, &r.ReceivedRatio, &r.PlanGap,
			&planMismatch, &overReceived, &r.UnitPrice, &r.Quantity, &lineMismatch); err != nil {
			return nil, sqlite.Classify(err)
		}
		r.PlanMismatch = planMismatch == 1
		r.OverReceived = overReceived == 1
		r.LineMismatch = lineMismatch == 1
		out = append(out, r)
	}
	return out, sqlite.Classify(rows.Err())
}
