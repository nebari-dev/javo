package apps_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestSharingWithBobShowsInBobsSharedList(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)

	if w := f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("put %d: %s", w.Code, w.Body)
	}
	w := f.Do("GET", "/api/shared", "", bob)
	if w.Code != http.StatusOK {
		t.Fatalf("shared %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.SharedJSON](t, w)
	if len(got.Items) != 1 {
		t.Fatalf("items %+v", got.Items)
	}
	it := got.Items[0]
	if it.Name != "report" || it.Via != "user" || it.Owner.ID != apitest.AliceID || it.Owner.DisplayName != "Alice" || it.URL != "https://apps.example.com/u/"+apitest.AliceID+"/report/" {
		t.Fatalf("item %+v", it)
	}
	// The owner's own list of shared apps does not include her own app.
	if got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", alice)); len(got.Items) != 0 {
		t.Fatalf("alice sees her own app as shared: %+v", got.Items)
	}
}

// A shared app carries when it was created, the same date its owner's list
// shows, so a viewer's own and shared apps sort together, newest first.
func TestSharedAppCarriesItsCreationDate(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice)
	type dated struct {
		Items []struct{ CreatedAt time.Time }
	}
	own := apitest.Decode[dated](t, f.Do("GET", "/api/workspaces", "", alice))
	shared := apitest.Decode[dated](t, f.Do("GET", "/api/shared", "", bob))
	if len(own.Items) != 1 || len(shared.Items) != 1 {
		t.Fatalf("own %+v, shared %+v", own.Items, shared.Items)
	}
	if own.Items[0].CreatedAt.IsZero() || !shared.Items[0].CreatedAt.Equal(own.Items[0].CreatedAt) {
		t.Fatalf("createdAt: own %v, shared %v", own.Items[0].CreatedAt, shared.Items[0].CreatedAt)
	}
}

// An app open to everyone signed in, or to anyone with the link, is shared
// with every signed-in person, so it is listed for them saying how; never for
// its owner, and never above the template's cap.
func TestAppsOpenToEveryoneAreListedSayingHow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ access, cap, via string }{
		{model.AccessSignedIn, model.AccessPublic, "signed-in"},
		{model.AccessPublic, model.AccessPublic, "public"},
		{model.AccessPublic, model.AccessRestricted, ""},
	} {
		report := gatewaytest.Workspace("report", apitest.AliceID, "Report")
		report.Sharing = model.Sharing{Access: tc.access}
		f := apitest.New(t, apitest.Options{Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Now()), apitest.SignedIn("bob", time.Now())}},
			apitest.SharableTemplate(tc.cap), report)
		got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)))
		if tc.via == "" {
			if len(got.Items) != 0 {
				t.Errorf("%s capped at %s: items %+v, want none", tc.access, tc.cap, got.Items)
			}
			continue
		}
		if len(got.Items) != 1 || got.Items[0].Via != tc.via || got.Items[0].Owner.ID != apitest.AliceID {
			t.Errorf("%s: items %+v, want one via %q", tc.access, got.Items, tc.via)
		}
		if mine := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.AliceID))); len(mine.Items) != 0 {
			t.Errorf("%s: alice sees her own app as shared: %+v", tc.access, mine.Items)
		}
	}
}

// A share made while the install had an apps host is not listed once apps
// are on the UI host without allowOneHost: it would not open.
func TestOneHostSharesAreNotListedWithoutAllowOneHost(t *testing.T) {
	t.Parallel()
	for _, allow := range []bool{false, true} {
		report := gatewaytest.Workspace("report", apitest.AliceID, "Report")
		report.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{apitest.BobID}}
		f := apitest.New(t, apitest.Options{AllowOneHost: allow, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Now()), apitest.SignedIn("bob", time.Now())}},
			apitest.SharableTemplate(model.AccessSignedIn), report)
		got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)))
		if want := map[bool]int{false: 0, true: 1}[allow]; len(got.Items) != want {
			t.Errorf("allowOneHost %v: items %+v, want %d", allow, got.Items, want)
		}
	}
}

func TestRemovingBobEndsHisSharedListing(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice)
	if len(apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", bob)).Items) != 1 {
		t.Fatal("setup: bob should see the app")
	}
	if w := f.Do("PUT", apitest.SharePath, `{"access":"Private","users":[],"groups":[]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("put %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", bob)); len(got.Items) != 0 {
		t.Fatalf("bob still sees %+v", got.Items)
	}
}

func TestSharedViaGroupNamesTheGroup(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":[],"groups":["/org/ml"]}`, f.Session(apitest.AliceID, "/org/ml"))
	got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID, "/org/ml")))
	if len(got.Items) != 1 || got.Items[0].Via != "group:/org/ml" {
		t.Fatalf("items %+v", got.Items)
	}
	if got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.CarolID))); len(got.Items) != 0 {
		t.Fatalf("carol is not in the group but sees %+v", got.Items)
	}
}

func TestBobCannotReadOrChangeAlicesSharing(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice)

	for _, c := range []struct{ method, body string }{
		{"PUT", `{"access":"SignedIn","users":[],"groups":[]}`},
		{"GET", ""},
	} {
		w := f.Do(c.method, apitest.SharePath, c.body, bob)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s as bob: %d %s", c.method, w.Code, w.Body)
		}
		if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(reasons.MsgAppNotFound) {
			t.Fatalf("title %q", p.Title)
		}
	}
	if got := f.GetWorkspace(apitest.AliceID, "report").Sharing; got.Access != "Restricted" {
		t.Fatalf("sharing changed to %+v", got)
	}
}

func TestShareAboveTemplateCapIsRefusedNamingTheCap(t *testing.T) {
	t.Parallel()
	lab := gatewaytest.Template("streamlit", "jupyterlab")
	lab.Spec.MaxSharing = model.AccessPrivate
	f := apitest.New(t, apitest.Options{Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}},
		lab, gatewaytest.Workspace("report", apitest.AliceID, "Report"))
	w := f.Do("PUT", apitest.SharePath, `{"access":"Public","users":[],"groups":[]}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.access")
	want := reasons.Msg(reasons.MsgShareTooOpen, reasons.Msg(reasons.MsgAccessPrivate))
	if !ok || msg != want {
		t.Fatalf("access message %q (found %v), want %q", msg, ok, want)
	}
}

func TestUnknownUserIsRefusedOnItsEntry(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	w := f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`","nobody-00000000"],"groups":[]}`, f.Session(apitest.AliceID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	p := apitest.Decode[apitest.Problem](t, w)
	if _, ok := p.FieldMessage("body.users[1]"); !ok {
		t.Fatalf("no error on users[1]: %+v", p.Errors)
	}
	if _, ok := p.FieldMessage("body.users[0]"); ok {
		t.Fatalf("known user flagged: %+v", p.Errors)
	}
	if got := f.GetWorkspace(apitest.AliceID, "report").Sharing; got.Access != model.AccessPrivate || len(got.Users) != 0 {
		t.Fatalf("sharing saved despite the error: %+v", got)
	}
}

func TestSharingNeedsAnAppsHostUnlessOneHostIsAllowed(t *testing.T) {
	t.Parallel()
	oneHost := config.URLs{UIHost: "javo.example.com", AppsHost: "javo.example.com"}
	cases := []struct {
		name   string
		opts   apitest.Options
		access string
		want   int
	}{
		{"one host public", apitest.Options{Urls: oneHost}, "Public", http.StatusUnprocessableEntity},
		{"one host signed in", apitest.Options{Urls: oneHost}, "SignedIn", http.StatusUnprocessableEntity},
		{"one host restricted", apitest.Options{Urls: oneHost}, "Restricted", http.StatusUnprocessableEntity},
		{"one host private", apitest.Options{Urls: oneHost}, "Private", http.StatusOK},
		{"ui host only, apps share it", apitest.Options{Urls: config.URLs{UIHost: "javo.example.com"}}, "Public", http.StatusUnprocessableEntity},
		{"no hosts configured", apitest.Options{}, "Restricted", http.StatusUnprocessableEntity},
		{"separate apps host", apitest.Options{Urls: apitest.SeparateHosts}, "Public", http.StatusOK},
		{"separate apps host restricted", apitest.Options{Urls: apitest.SeparateHosts}, "Restricted", http.StatusOK},
		{"subdomain apps", apitest.Options{Urls: config.URLs{Mode: "subdomain", UIHost: "javo.example.com", AppsDomain: "apps.example.com"}}, "Public", http.StatusOK},
		{"one host allowed", apitest.Options{AllowOneHost: true}, "Public", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opts.People = []apitest.TestPerson{apitest.SignedIn("alice", time.Time{})}
			f := apitest.New(t, c.opts, apitest.SharableTemplate(model.AccessPublic), gatewaytest.Workspace("report", apitest.AliceID, "Report"))
			w := f.Do("PUT", apitest.SharePath, `{"access":"`+c.access+`","users":[],"groups":[]}`, f.Session(apitest.AliceID))
			if w.Code != c.want {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if c.want == http.StatusUnprocessableEntity {
				if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.access"); msg != reasons.Msg(reasons.MsgShareNeedsAppsHost) {
					t.Fatalf("message %q", msg)
				}
			}
		})
	}
}

func TestTransientIdentityErrorIsNotShownAsInactive(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	alice := f.Session(apitest.AliceID)
	if w := f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("put %d: %s", w.Code, w.Body)
	}
	f.People.Fail.Store(true)
	if w := f.Do("GET", apitest.SharePath, "", alice); w.Code != http.StatusInternalServerError {
		t.Fatalf("sharing status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)); w.Code != http.StatusInternalServerError {
		t.Fatalf("shared status %d: %s", w.Code, w.Body)
	}
}

func TestSharedShowsOwnerIDWhenOwnerIdentityIsGone(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, f.Session(apitest.AliceID))
	f.DeletePerson(apitest.AliceID)
	got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)))
	if len(got.Items) != 1 || got.Items[0].Owner.DisplayName != apitest.AliceID {
		t.Fatalf("items %+v", got.Items)
	}
}

func TestGetSharingMarksStaleAndMissingPeopleInactive(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := apitest.NewSharingFixture(t, apitest.Options{StaleAfter: 24 * time.Hour, Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("dave", now.Add(-48*time.Hour))}})
	alice := f.Session(apitest.AliceID, "/org/ml")
	body := `{"access":"Restricted","users":["` + apitest.BobID + `","` + apitest.DaveID + `"],"groups":["/org/ml"]}`
	if w := f.Do("PUT", apitest.SharePath, body, alice); w.Code != http.StatusOK {
		t.Fatalf("put %d: %s", w.Code, w.Body)
	}
	// carol-style removal: bob is deleted after the share.
	f.DeletePerson(apitest.BobID)
	got := apitest.Decode[apitest.SharingJSON](t, f.Do("GET", apitest.SharePath, "", alice))
	if got.Access != "Restricted" || got.MaxAccess != model.AccessSignedIn {
		t.Fatalf("access %q max %q", got.Access, got.MaxAccess)
	}
	if got.Link != "https://apps.example.com/u/"+apitest.AliceID+"/report/" {
		t.Fatalf("link %q", got.Link)
	}
	active := map[string]bool{}
	for _, u := range got.Users {
		active[u.ID] = u.Active
	}
	if len(got.Users) != 2 || active[apitest.BobID] || active[apitest.DaveID] {
		t.Fatalf("users %+v", got.Users)
	}
	if len(got.Groups) != 1 || got.Groups[0].Value != "/org/ml" || got.Groups[0].Label != "ml" {
		t.Fatalf("groups %+v", got.Groups)
	}
	f2 := apitest.NewSharingFixture(t, apitest.Options{StaleAfter: 24 * time.Hour, Urls: apitest.SeparateHosts})
	f2.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, f2.Session(apitest.AliceID))
	got = apitest.Decode[apitest.SharingJSON](t, f2.Do("GET", apitest.SharePath, "", f2.Session(apitest.AliceID)))
	if len(got.Users) != 1 || !got.Users[0].Active || got.Users[0].DisplayName != "Bob" || got.Users[0].Email != "bob@example.com" {
		t.Fatalf("recent login should be active: %+v", got.Users)
	}
}

func TestDirectory(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{})
	get := func(q string, groups ...string) apitest.DirectoryJSON {
		t.Helper()
		w := f.Do("GET", "/api/directory?q="+q, "", f.Session(apitest.CarolID, groups...))
		if w.Code != http.StatusOK {
			t.Fatalf("q=%s: %d %s", q, w.Code, w.Body)
		}
		return apitest.Decode[apitest.DirectoryJSON](t, w)
	}
	if got := get("bo", "/org/bo"); len(got.People) != 0 || len(got.Groups) != 0 {
		t.Fatalf("two characters must list nothing: %+v", got)
	}
	got := get("bob")
	if len(got.People) != 1 || got.People[0].ID != apitest.BobID || got.People[0].Email != "bob@example.com" || got.People[0].DisplayName != "Bob" {
		t.Fatalf("people %+v", got.People)
	}
	if got := get("org/ml"); len(got.Groups) != 0 {
		t.Fatalf("carol is not in /org/ml but sees %+v", got.Groups)
	}
	if got := get("org/ml", "/org/ml", "/org/data"); len(got.Groups) != 1 || got.Groups[0].Value != "/org/ml" {
		t.Fatalf("groups %+v", got.Groups)
	}
}

// Nobody can walk the directory: each person gets a burst of searches, then
// one a second.
func TestDirectorySearchesAreRateLimited(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{})
	c := f.Session(apitest.CarolID)
	for i := range apps.DirectoryBurst {
		if w := f.Do("GET", "/api/directory?q=bob", "", c); w.Code != http.StatusOK {
			t.Fatalf("search %d: %d", i, w.Code)
		}
	}
	if w := f.Do("GET", "/api/directory?q=bob", "", c); w.Code != http.StatusTooManyRequests {
		t.Fatalf("search past the burst: %d %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/directory?q=bob", "", f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("another person's search: %d", w.Code)
	}
}

func TestDirectoryReturnsAtMostTenPeople(t *testing.T) {
	t.Parallel()
	var people []apitest.TestPerson
	for i := range 12 {
		people = append(people, apitest.SignedIn("pat"+string(rune('a'+i)), time.Time{}))
	}
	f := apitest.New(t, apitest.Options{People: people})
	got := apitest.Decode[apitest.DirectoryJSON](t, f.Do("GET", "/api/directory?q=pat", "", f.Session(apitest.AliceID)))
	if len(got.People) != 10 {
		t.Fatalf("people %d", len(got.People))
	}
}

func TestSharingViewCarriesTheWordsTheUIShows(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := apitest.NewSharingFixture(t, apitest.Options{StaleAfter: 24 * time.Hour, Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("dave", now.Add(-48*time.Hour))}})
	alice := f.Session(apitest.AliceID)
	body := `{"access":"Restricted","users":["` + apitest.BobID + `","` + apitest.DaveID + `"],"groups":[]}`
	if w := f.Do("PUT", apitest.SharePath, body, alice); w.Code != http.StatusOK {
		t.Fatalf("put %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.SharingJSON](t, f.Do("GET", apitest.SharePath, "", alice))

	notes := map[string]string{}
	for _, u := range got.Users {
		notes[u.ID] = u.Note
	}
	if notes[apitest.BobID] != "" || notes[apitest.DaveID] != reasons.Msg(reasons.MsgPersonInactive) {
		t.Fatalf("notes %+v", notes)
	}
	wantLevels := map[string]string{
		model.AccessRestricted: reasons.Msg(reasons.MsgAccessLabelRestricted),
		model.AccessSignedIn:   reasons.Msg(reasons.MsgAccessLabelSignedIn),
		model.AccessPublic:     reasons.Msg(reasons.MsgAccessLabelPublic),
	}
	if len(got.Levels) != 3 {
		t.Fatalf("levels %+v", got.Levels)
	}
	for _, l := range got.Levels {
		if wantLevels[l.Value] != l.Label {
			t.Fatalf("level %+v, want label %q", l, wantLevels[l.Value])
		}
	}
	// This template allows SignedIn at most, so the cap is explained.
	if want := reasons.Msg(reasons.MsgShareTooOpen, reasons.Msg(reasons.MsgAccessSignedIn)); got.TooOpen != want {
		t.Fatalf("tooOpen %q, want %q", got.TooOpen, want)
	}

	open := apitest.New(t, apitest.Options{Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("alice", now)}},
		apitest.SharableTemplate(model.AccessPublic), gatewaytest.Workspace("report", apitest.AliceID, "Report"))
	if got := apitest.Decode[apitest.SharingJSON](t, open.Do("GET", apitest.SharePath, "", open.Session(apitest.AliceID))); got.TooOpen != "" {
		t.Fatalf("nothing is capped but tooOpen is %q", got.TooOpen)
	}
}

func TestSharedListSaysSoWhenNothingIsShared(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)))
	if want := reasons.Msg(reasons.MsgNothingShared); got.Empty != want {
		t.Fatalf("empty %q, want %q", got.Empty, want)
	}
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, f.Session(apitest.AliceID))
	if got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID))); got.Empty != "" {
		t.Fatalf("empty %q though one app is shared", got.Empty)
	}
}

// shareFixture serves Alice's app "report" from tpl, which allows Public.
func shareFixture(t *testing.T, tpl *model.Template) *apitest.Fixture {
	t.Helper()
	tpl.Spec.MaxSharing = model.AccessPublic
	return apitest.New(t, apitest.Options{Urls: apitest.SeparateHosts, People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{}), apitest.SignedIn("bob", time.Time{})}},
		tpl, gatewaytest.Workspace("report", apitest.AliceID, "Report"))
}

// The Terminal, JupyterLab and the coding agents hand whoever opens them code
// running as the owner, so they stay private whatever their template allows,
// even through the API.
func TestPrivateOnlyKindsRefuseSharingWhateverTheTemplateAllows(t *testing.T) {
	t.Parallel()
	agent := gatewaytest.Template("streamlit", "custom")
	agent.Spec.Category = "agents"
	for name, tpl := range map[string]*model.Template{
		"terminal":        gatewaytest.Template("streamlit", "terminal"),
		"jupyterlab":      gatewaytest.Template("streamlit", "jupyterlab"),
		"claude-code":     gatewaytest.Template("streamlit", "claude-code"),
		"pi":              gatewaytest.Template("streamlit", "pi"),
		"herdr":           gatewaytest.Template("streamlit", "herdr"),
		"agents category": agent,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := shareFixture(t, tpl)
			alice := f.Session(apitest.AliceID)
			w := f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			want := reasons.Msg(reasons.MsgShareTooOpen, reasons.Msg(reasons.MsgAccessPrivate))
			if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.access"); msg != want {
				t.Fatalf("message %q, want %q", msg, want)
			}
			if got := apitest.Decode[apitest.SharingJSON](t, f.Do("GET", apitest.SharePath, "", alice)); got.MaxAccess != model.AccessPrivate {
				t.Fatalf("maxAccess %q", got.MaxAccess)
			}
		})
	}
}

// Every other kind can be shared, and the Sharing tab says what that hands
// the people it is shared with.
func TestShareableKindsSayWhatSharingExposes(t *testing.T) {
	t.Parallel()
	site := gatewaytest.Template("streamlit", "static")
	site.Spec.Build = &model.TemplateBuild{Image: "example.org/node:1", Command: "npm run build", ServeDir: "/srv"}
	for name, c := range map[string]struct {
		tpl  *model.Template
		want reasons.MessageCode
		max  string
	}{
		"vscode":    {gatewaytest.Template("streamlit", "vscode"), reasons.MsgShareExposesCode, model.AccessRestricted},
		"rstudio":   {gatewaytest.Template("streamlit", "rstudio"), reasons.MsgShareExposesCode, model.AccessRestricted},
		"marimo":    {gatewaytest.Template("streamlit", "marimo"), reasons.MsgShareExposesCode, model.AccessRestricted},
		"pluto":     {gatewaytest.Template("streamlit", "pluto"), reasons.MsgShareExposesCode, model.AccessRestricted},
		"streamlit": {gatewaytest.Template("streamlit", "streamlit"), reasons.MsgShareExposesApp, model.AccessPublic},
		"voila":     {gatewaytest.Template("streamlit", "voila"), reasons.MsgShareExposesApp, model.AccessPublic},
		"fastapi":   {gatewaytest.Template("streamlit", "fastapi"), reasons.MsgShareExposesApp, model.AccessPublic},
		"container": {gatewaytest.Template("streamlit", "container"), reasons.MsgShareExposesApp, model.AccessPublic},
		"static":    {site, reasons.MsgShareExposesSite, model.AccessPublic},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := shareFixture(t, c.tpl)
			w := f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, f.Session(apitest.AliceID))
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if got := apitest.Decode[apitest.SharingJSON](t, w); got.MaxAccess != c.max || got.Exposes != reasons.Msg(c.want) {
				t.Fatalf("maxAccess %q, exposes %q, want %q and %q", got.MaxAccess, got.Exposes, c.max, reasons.Msg(c.want))
			}
		})
	}
}

// Apps that run any code as their owner (IDEs and editable notebooks) may be
// shared only with chosen people, never everyone signed in or anyone with the
// link, whatever their template allows.
func TestCodeEditorsShareOnlyWithChosenPeople(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"vscode", "rstudio", "marimo", "pluto"} {
		for _, access := range []string{model.AccessSignedIn, model.AccessPublic} {
			t.Run(kind+" "+access, func(t *testing.T) {
				t.Parallel()
				f := shareFixture(t, gatewaytest.Template("streamlit", kind))
				w := f.Do("PUT", apitest.SharePath, `{"access":"`+access+`","users":[],"groups":[]}`, f.Session(apitest.AliceID))
				if w.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status %d: %s", w.Code, w.Body)
				}
				want := reasons.Msg(reasons.MsgShareTooOpen, reasons.Msg(reasons.MsgAccessRestricted))
				if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.access"); msg != want {
					t.Fatalf("message %q, want %q", msg, want)
				}
			})
		}
	}
}
