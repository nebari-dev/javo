package authoidc_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/reasons"
)

func metav1Duration(d time.Duration) metav1.Duration { return metav1.Duration{Duration: d} }

// title is the catalog title of a reason, which the sign-in pages show.
func title(code reasons.Code) string {
	r, ok := reasons.Lookup(code)
	if !ok {
		panic("no catalog entry for " + string(code))
	}
	return r.Title
}
