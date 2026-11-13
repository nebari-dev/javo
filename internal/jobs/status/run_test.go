package status

import (
	"testing"

	"github.com/nebari-dev/javo/internal/reasons"
)

func TestEmittedReasonsAreCataloged(t *testing.T) {
	for _, c := range EmittedReasons() {
		if _, ok := reasons.Lookup(reasons.Code(c)); !ok {
			t.Errorf("%s emitted but missing from the catalog", c)
		}
	}
}
