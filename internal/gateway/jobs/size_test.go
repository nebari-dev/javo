package jobs_test

import (
	"fmt"
	"net/http"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A job made without a size is stored at the default size (so a later change
// of the default does not change its runs); one made with a size keeps it.
func TestCreateJobStoresTheChosenOrDefaultSize(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{Sizes: apitest.TestCatalog(t)})
	alice := f.Session(apitest.AliceID)
	w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, ""), alice)
	if w.Code != http.StatusCreated || apitest.Decode[apitest.JobSizeJSON](t, w).Size != "small" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredJob(apitest.AliceID, "nightly-sales").Size; got != "small" {
		t.Fatalf("stored size %q", got)
	}
	body := `{"displayName":"Big","template":"lab","engine":"papermill","source":"reports/sales.ipynb","size":"medium"}`
	if w := f.Do("POST", "/api/jobs", body, alice); w.Code != http.StatusCreated || apitest.Decode[apitest.JobSizeJSON](t, w).Size != "medium" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredJob(apitest.AliceID, "big").Size; got != "medium" {
		t.Fatalf("stored size %q", got)
	}
}

// A job is held to the same size checks as an app: a size not on the list,
// one no machine can hold, and a custom amount the person may not ask for
// are refused and nothing is made.
func TestCreateJobRefusesSizesAppsCannotUse(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{Sizes: apitest.FitCatalog(t, "small")}, apitest.SingleNode())
	alice := f.Session(apitest.AliceID)
	for _, tc := range []struct {
		name, extra, field, msg string
		status                  int
	}{
		{"unknown", `,"size":"huge"`, "body.size", reasons.Msg(reasons.MsgSizeUnavailable), http.StatusUnprocessableEntity},
		{"too big", `,"size":"large"`, "body.size", apitest.TooBigOnSingleNode, http.StatusUnprocessableEntity},
		{"custom not allowed", `,"resources":{"cpu":2,"memory":4294967296}`, "", reasons.Msg(reasons.MsgCustomSizeNotAllowed), http.StatusForbidden},
	} {
		w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, tc.extra), alice)
		if w.Code != tc.status {
			t.Errorf("%s: status %d: %s", tc.name, w.Code, w.Body)
			continue
		}
		p := apitest.Decode[apitest.Problem](t, w)
		if msg, _ := p.FieldMessage(tc.field); tc.field != "" && msg != tc.msg || tc.field == "" && p.Title != tc.msg {
			t.Errorf("%s: problem %s", tc.name, w.Body)
		}
	}
	if len(f.Jobs()) != 0 {
		t.Fatal("a job was made")
	}
}

// Change size moves a job to another size or a custom amount from its next
// run on; the job's other settings and its edits keep the size.
func TestChangeJobSize(t *testing.T) {
	t.Parallel()
	j := apitest.JobObj(apitest.AliceID, "nightly-sales")
	j.Size = "small"
	f := apitest.JobFixture(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode(), j)
	admin := f.Session("root-rrrrrrrr", "javo-admins")
	alice := f.Session(apitest.AliceID)

	w := f.Do("PUT", "/api/jobs/nightly-sales/size", `{"size":"medium"}`, alice)
	if w.Code != http.StatusOK || apitest.Decode[apitest.JobSizeJSON](t, w).Size != "medium" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.StoredJob(apitest.AliceID, "nightly-sales").Size; got != "medium" {
		t.Fatalf("stored size %q", got)
	}

	w = f.Do("PUT", "/api/jobs/nightly-sales/size", `{"size":"gpu"}`, alice)
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.size"); w.Code != http.StatusUnprocessableEntity || msg != reasons.Msg(reasons.MsgSizeUnavailable) {
		t.Fatalf("a size alice may not use: status %d: %s", w.Code, w.Body)
	}

	w = f.Do("PUT", "/api/jobs/nightly-sales/size?owner="+apitest.AliceID, `{"resources":{"cpu":2.5,"memory":6442450944}}`, admin)
	got := apitest.Decode[apitest.JobSizeJSON](t, w)
	if w.Code != http.StatusOK || got.Size != "" || got.Resources == nil || got.Resources.CPU != 2.5 || got.Resources.Memory != 6<<30 {
		t.Fatalf("custom amount: status %d: %s", w.Code, w.Body)
	}
	stored := f.StoredJob(apitest.AliceID, "nightly-sales")
	if stored.Size != "" || stored.Resources == nil || !stored.Resources.CPU.Equal(resource.MustParse("2500m")) {
		t.Fatalf("stored job = %+v", stored)
	}

	edit := `{"displayName":"nightly-sales","template":"lab","engine":"papermill","source":"reports/other.ipynb"}`
	if w := f.Do("PUT", "/api/jobs/nightly-sales", edit, alice); w.Code != http.StatusOK || apitest.Decode[apitest.JobSizeJSON](t, w).Resources == nil {
		t.Fatalf("edit: status %d: %s", w.Code, w.Body)
	}

	if w := f.Do("PUT", "/api/jobs/nightly-sales/size", `{"size":"small"}`, f.Session(apitest.BobID)); w.Code != http.StatusNotFound {
		t.Fatalf("other user's job: status %d", w.Code)
	}
}

// A job stored without a size (made before jobs had one) shows the default
// its runs get.
func TestJobWithoutSizeShowsTheDefault(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{Sizes: apitest.TestCatalog(t)}, apitest.JobObj(apitest.AliceID, "nightly-sales"))
	w := f.Do("GET", "/api/jobs/nightly-sales", "", f.Session(apitest.AliceID))
	if w.Code != http.StatusOK || apitest.Decode[apitest.JobSizeJSON](t, w).Size != "small" {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

// A job made without a size runs at its template's default amount, as an
// app does: a custom amount for a person who may ask for one, else the
// smallest size that holds it.
func TestCreateJobWithoutASizeUsesTheTemplatesDefault(t *testing.T) {
	t.Parallel()
	tpl := apitest.SampleJobTemplate()
	tpl.Spec.DefaultResources = &model.TemplateAmount{CPU: resource.MustParse("2"), Memory: resource.MustParse("4Gi")}
	f := apitest.JobFixture(t, apitest.Options{Sizes: apitest.CustomCatalog(t)}, apitest.SingleNode(), tpl)
	if w := f.Do("POST", "/api/job-samples/jobs-lab", "", f.Session(apitest.AliceID)); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if j := f.StoredJob(apitest.AliceID, "print-the-time"); j.Size != "medium" || j.Resources != nil {
		t.Fatalf("stored size %q, resources %+v", j.Size, j.Resources)
	}
	if w := f.Do("POST", "/api/job-samples/jobs-lab", "", f.Session("bob-bbbbbbbb", "ds")); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if j := f.StoredJob("bob-bbbbbbbb", "print-the-time"); j.Size != "" || j.Resources == nil || !j.Resources.CPU.Equal(resource.MustParse("2")) || !j.Resources.Memory.Equal(resource.MustParse("4Gi")) {
		t.Fatalf("stored size %q, resources %+v", j.Size, j.Resources)
	}
}
