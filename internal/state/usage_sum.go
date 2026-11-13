package state

import (
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"
	"prism/internal/model"
)

func init() {
	// SQLite SUM throws on integer overflow; TOTAL silently uses floats.
	// Usage reports and compaction need exact integers up to the int64 cap.
	sqlite.MustRegisterFunction("prism_usage_sum", &sqlite.FunctionImpl{
		NArgs: 1, Deterministic: true,
		MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return new(usageSum), nil },
	})
}

type usageSum struct{ total int64 }

func (s *usageSum) Step(_ *sqlite.FunctionContext, values []driver.Value) error {
	if values[0] == nil {
		return nil
	}
	value, ok := values[0].(int64)
	if !ok || value < 0 {
		return fmt.Errorf("usage must be a nonnegative integer")
	}
	s.total = model.SaturatingUsageAdd(s.total, value)
	return nil
}
func (*usageSum) WindowInverse(*sqlite.FunctionContext, []driver.Value) error {
	return fmt.Errorf("usage sum does not support sliding windows")
}
func (s *usageSum) WindowValue(*sqlite.FunctionContext) (driver.Value, error) { return s.total, nil }
func (*usageSum) Final(*sqlite.FunctionContext)                               {}

func validateUsageRows(rows []model.UsageRow) error {
	for _, row := range rows {
		if row.IngressBytes < 0 || row.EgressBytes < 0 || row.Requests < 0 || row.Errors < 0 {
			return fmt.Errorf("%w: usage must be nonnegative", ErrConflict)
		}
	}
	return nil
}
