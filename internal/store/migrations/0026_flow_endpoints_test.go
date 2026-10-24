package migrations_test

import (
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

// The step row of a start or an end node is not work, but the engine still
// records an execution for both: the kind check has to know the two new
// kinds, or the engine could not write the row of a flow's first and last
// node.
func TestFlowEndpointsMigration(t *testing.T) {
	db := pgtest.FreshDB(t)

	var definition string
	if err := db.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'step_runs_kind_check'`).Scan(&definition); err != nil {
		t.Fatalf("read the kind constraint: %v", err)
	}
	for _, kind := range []string{"agent", "integration", "checkpoint", "loop", "merge", "start", "end"} {
		if !strings.Contains(definition, "'"+kind+"'") {
			t.Errorf("the kind constraint does not allow %q: %s", kind, definition)
		}
	}
}
