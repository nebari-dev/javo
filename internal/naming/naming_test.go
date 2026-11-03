package naming

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

const testIssuer = "https://kc.example.com/realms/javo"

// testID is the identity name of a person whose subject is their username.
func testID(username string) string { return IdentityName(username, testIssuer, username, 8) }

func TestIdentityName(t *testing.T) {
	tests := []struct {
		name     string
		username string
		prefix   string
	}{
		{"plain", "alice", "alice-"},
		{"email", "Alice.O'Neil@Example.com", "alice-o-neil"},
		{"long", strings.Repeat("a", 200), "aaaa"},
		{"non-ascii only", "名前", "u-"},
		{"empty", "", "u-"},
		{"spaces", "alice smith", "alice-smith"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, hexLen := range []int{8, 12} {
				got := IdentityName(tt.username, testIssuer, "f3a1", hexLen)
				if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
					t.Fatalf("IdentityName(%q)=%q invalid: %v", tt.username, got, errs)
				}
				if !strings.HasPrefix(got, tt.prefix) {
					t.Fatalf("IdentityName(%q)=%q, want prefix %q", tt.username, got, tt.prefix)
				}
			}
		})
	}
}

func TestIdentityNameHashCoversIssuerAndSubject(t *testing.T) {
	base := IdentityName("alice", testIssuer, "f3a1", 8)
	if base != IdentityName("alice", testIssuer, "f3a1", 8) {
		t.Fatal("must be stable")
	}
	if base == IdentityName("alice", "https://dex.example.com", "f3a1", 8) {
		t.Fatal("two issuers emitting the same username must differ")
	}
	if base == IdentityName("alice", testIssuer, "f3a2", 8) {
		t.Fatal("two subjects must differ")
	}
	if base == IdentityName("alice", testIssuer+"f", "3a1", 8) {
		t.Fatal("the issuer and subject boundary must be part of the hash")
	}
	if got := IdentityName("alice", testIssuer, "f3a1", 12); !strings.HasPrefix(got, "alice-") || len(got) != len("alice-")+12 {
		t.Fatalf("12-hex form = %q", got)
	}
}

func TestWorkspaceName(t *testing.T) {
	got, err := WorkspaceName("Sales app")
	if err != nil || got != "sales-app" {
		t.Fatalf("got %q, %v", got, err)
	}
	long, err := WorkspaceName(strings.Repeat("a", 100) + "!")
	if err != nil || len(validation.IsDNS1123Label(long)) > 0 {
		t.Fatalf("long name invalid: %q, %v", long, err)
	}
	_, err = WorkspaceName("!!!")
	if err == nil || err.Error() != "Use at least one letter or number in the name" {
		t.Fatalf("got err %v", err)
	}
}

func TestHomeClaimName(t *testing.T) {
	if got := HomeClaimName("alice-1234abcd"); got != "home-alice-1234abcd" {
		t.Fatalf("got %q", got)
	}
}

// The first claim of a home keeps the name it had before generations existed,
// so a claim made earlier is adopted by name; later ones add the generation.
func TestHomeClaimNameGen(t *testing.T) {
	for gen, want := range map[int]string{0: "home-alice-1234abcd", 1: "home-alice-1234abcd", 2: "home-alice-1234abcd-2", 12: "home-alice-1234abcd-12"} {
		if got := HomeClaimNameGen("alice-1234abcd", gen); got != want {
			t.Errorf("gen %d = %q, want %q", gen, got, want)
		}
	}
}

// HomeClaimGen reads the generation back from a claim name of the same owner.
func TestHomeClaimGen(t *testing.T) {
	for claim, want := range map[string]int{
		"home-alice-1234abcd":    1,
		"home-alice-1234abcd-2":  2,
		"home-alice-1234abcd-12": 12,
		"home-bob-1234abcd":      0,
		"home-alice-1234abcd-x":  0,
		"home-alice-1234abcd-":   0,
	} {
		if got := HomeClaimGen("alice-1234abcd", claim); got != want {
			t.Errorf("%q = %d, want %d", claim, got, want)
		}
	}
}

// File pods are told apart by labels, not by a name prefix, so any name works.
func TestWorkspaceNameAllowsProductPrefix(t *testing.T) {
	if got, err := WorkspaceName("javo files"); err != nil || got != "javo-files" {
		t.Errorf("javo files = %q, %v", got, err)
	}
}

func TestObjectNameSeparatesUsers(t *testing.T) {
	alice, bob := testID("alice"), testID("bob")
	if ObjectName(alice, "sales") == ObjectName(bob, "sales") {
		t.Fatal("two users' apps with the same name must get different object names")
	}
	if got := ObjectName(alice, "sales"); got != alice+"-sales" {
		t.Fatalf("short names stay readable: got %q", got)
	}
	if ObjectName(alice, "sales") != ObjectName(alice, "sales") {
		t.Fatal("must be stable")
	}
}

func TestObjectNameIsAValidServiceName(t *testing.T) {
	long := strings.Repeat("b", 63)
	for _, tc := range []struct{ slug, name string }{
		{testID("alice"), "sales"},
		{testID(strings.Repeat("a", 200)), long},
		{testID("1bob"), "x"},
		{testID("名前"), long},
	} {
		got := ObjectName(tc.slug, tc.name)
		// A Service carries this name, so it must be a DNS-1035 label.
		if errs := validation.IsDNS1035Label(got); len(errs) > 0 {
			t.Errorf("ObjectName(%q, %q)=%q invalid: %v", tc.slug, tc.name, got, errs)
		}
	}
}

func TestObjectNameLongNamesStayDistinct(t *testing.T) {
	slug := testID(strings.Repeat("a", 200))
	a := ObjectName(slug, strings.Repeat("b", 62)+"c")
	b := ObjectName(slug, strings.Repeat("b", 62)+"d")
	if a == b {
		t.Fatalf("hashed long names collide: %q", a)
	}
}

func TestNamespaceNameStaysADNSLabel(t *testing.T) {
	if got := NamespaceName("javo", "alice-1a2b3c4d"); got != "javo-alice-1a2b3c4d" {
		t.Fatalf("short name = %q", got)
	}
	long := strings.Repeat("x", 70)
	got := NamespaceName("javo", long)
	if errs := validation.IsDNS1123Label(got); len(errs) > 0 || len(got) > 63 {
		t.Fatalf("%q is not a DNS label: %v", got, errs)
	}
	// Two long names that agree up to the cut stay different.
	if other := NamespaceName("javo", long+"y"); other == got {
		t.Fatalf("different names gave the same namespace %q", got)
	}
}

func TestObjectNameMaxFitsLimitAndStaysDistinct(t *testing.T) {
	slug := "alice-1a2b3c4d"
	long := strings.Repeat("n", 60)
	got := ObjectNameMax(slug, long, 52)
	if len(got) > 52 {
		t.Fatalf("len(%q)=%d, want <= 52", got, len(got))
	}
	if errs := validation.IsDNS1035Label(got); len(errs) > 0 {
		t.Fatalf("%q invalid: %v", got, errs)
	}
	if got != ObjectNameMax(slug, long, 52) {
		t.Fatal("not stable across calls")
	}
	if got == ObjectNameMax(slug, long[:59]+"x", 52) {
		t.Fatalf("different names collide: %q", got)
	}
	if ObjectNameMax(slug, "sales", 52) != slug+"-sales" {
		t.Fatalf("short name should stay readable, got %q", ObjectNameMax(slug, "sales", 52))
	}
}

func TestIdentityReadableUndoesIdentityName(t *testing.T) {
	for username, want := range map[string]string{
		"alice":                    "alice",
		"Alice.O'Neil@Example.com": "alice-o-neil-example",
		"mary-jane":                "mary-jane",
		"名前":                       "u",
	} {
		for _, hexLen := range []int{8, 12} {
			if got := IdentityReadable(IdentityName(username, testIssuer, "f3a1", hexLen)); got != want {
				t.Errorf("IdentityReadable(IdentityName(%q)) = %q, want %q", username, got, want)
			}
		}
	}
	// A name without the hash suffix (a test fixture) is kept whole.
	if got := IdentityReadable("alice"); got != "alice" {
		t.Errorf("IdentityReadable(alice) = %q", got)
	}
}

// A shared folder name is at most 56 characters, so the claim name fits a
// label value.
func TestFolderClaimNameOfLongestFolderFitsALabel(t *testing.T) {
	if got := FolderClaimName(strings.Repeat("a", 56)); len(got) > 63 {
		t.Fatalf("claim name is %d characters, want at most 63", len(got))
	}
}

func TestGroupDir(t *testing.T) {
	long := strings.Repeat("x", 40)
	for _, tc := range []struct{ group, want string }{
		{"research", "research"},
		{"/developer", "developer"},
		{"/org/data-science", "data-science"},
		{"/org/data-science/", "data-science"},
	} {
		if got := GroupDir(tc.group); got != tc.want {
			t.Errorf("GroupDir(%q) = %q, want %q", tc.group, got, tc.want)
		}
	}
	// A lossy slug (case, spaces, unicode, length, nothing readable) gets the
	// hash of the verbatim group, so it never equals a plain group's dir.
	for _, g := range []string{"/Data Team", "/org/Data Team", "/caf\u00e9", long, "/org/" + long, "/", ""} {
		got := GroupDir(g)
		if !strings.Contains(got, "-") || len(got) > maxGroupDir+1+hashLen {
			t.Errorf("GroupDir(%q) = %q, want a slug with a hash suffix", g, got)
		}
		if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
			t.Errorf("GroupDir(%q) = %q: %v", g, got, errs)
		}
	}
	if GroupDir("/Data Team") == GroupDir("/data-team") {
		t.Error("a lossy slug collides with the plain group")
	}
	if GroupDir("/a/Data Team") == GroupDir("/b/Data Team") {
		t.Error("two lossy groups share a dir")
	}
}

func TestPreviewName(t *testing.T) {
	if got := PreviewName("site", 12); got != "site--pr-12" {
		t.Fatalf("PreviewName(site, 12) = %q", got)
	}
}

func TestPreviewNameFitsALabelAndStaysDistinct(t *testing.T) {
	long := strings.Repeat("a", 63)
	got := PreviewName(long, 2147483647)
	if len(got) != 63 || !strings.HasSuffix(got, "--pr-2147483647") {
		t.Fatalf("PreviewName(long) = %q (%d chars)", got, len(got))
	}
	if errs := validation.IsDNS1123Label(got); len(errs) > 0 {
		t.Fatalf("not a DNS label: %v", errs)
	}
	// Two parents that agree up to the cut keep different preview names.
	other := strings.Repeat("a", 62) + "b"
	if PreviewName(long, 7) == PreviewName(other, 7) {
		t.Fatal("long parents that differ after the cut share a preview name")
	}
	// A name that fits is never changed, so the plain form stays readable.
	if got := PreviewName(strings.Repeat("a", 40), 7); got != strings.Repeat("a", 40)+"--pr-7" {
		t.Fatalf("short parent was altered: %q", got)
	}
	// A cut that lands on a dash does not leave "---".
	dashy := strings.Repeat("a", 45) + "-" + strings.Repeat("b", 17)
	if p := PreviewName(dashy, 123456); strings.Contains(p, "---") || len(p) > 63 {
		t.Fatalf("PreviewName(dashy) = %q", p)
	}
}

// A person cannot pick a name that equals a preview's: slugs never hold "--".
func TestWorkspaceNameNeverContainsDoubleDash(t *testing.T) {
	for _, in := range []string{"a--b", "a - - b", "site--pr-1", "--x--", "a_-_b", "Café -- Ünï"} {
		got, err := WorkspaceName(in)
		if err != nil {
			continue
		}
		if strings.Contains(got, "--") {
			t.Errorf("WorkspaceName(%q) = %q contains --", in, got)
		}
	}
}
