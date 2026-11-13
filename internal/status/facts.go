package status

import (
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// downloadPrefixes start the line that says how the image got there.
var downloadPrefixes = []string{"already on the node", "downloaded"}

// stepFacts is, per condition type, what the executor reported about how
// that step went for this workload, plus how its image got there. prev is
// the app's recorded conditions: the platform forgets a download after a
// while, so the line recorded for this same workload is kept.
func stepFacts(st *workload.State, prev []metav1.Condition) map[string][]string {
	if st == nil {
		return nil
	}
	line := downloadLine(st, prev)
	if line == "" {
		return st.Facts
	}
	out := make(map[string][]string, len(st.Facts)+1)
	for step, lines := range st.Facts {
		out[step] = lines
	}
	out[model.ConditionImageReady] = append(slices.Clone(out[model.ConditionImageReady]), line)
	return out
}

func downloadLine(st *workload.State, prev []metav1.Condition) string {
	if p := st.Pull; p != nil {
		if p.Cached {
			return "already on the node, not downloaded"
		}
		line := "downloaded"
		if p.Took != "" {
			line += " in " + p.Took
		}
		if p.Bytes > 0 {
			line += ", " + megabytes(p.Bytes)
		}
		return line
	}
	i := slices.IndexFunc(prev, func(c metav1.Condition) bool { return c.Type == model.ConditionImageReady })
	if i < 0 || prev[i].Status != metav1.ConditionTrue || prev[i].LastTransitionTime.Time.Before(st.Created) {
		return ""
	}
	for _, l := range strings.Split(prev[i].Message, "\n") {
		for _, pre := range downloadPrefixes {
			if strings.HasPrefix(l, pre) {
				return l
			}
		}
	}
	return ""
}

// megabytes is n bytes in whole MB (10^6), the unit registries show.
func megabytes(n int64) string {
	return fmt.Sprintf("%d MB", (n+500_000)/1_000_000)
}
