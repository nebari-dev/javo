package apitest

import (
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
)

// RolesConfig: javo-admins are platform admins, tmpl-admins template admins,
// and only /org/members (plus the admins) are members.
func RolesConfig() *config.Roles {
	var r config.Roles
	r.PlatformAdmin.Groups = []string{"javo-admins"}
	r.TemplateAdmin.Groups = []string{"tmpl-admins"}
	r.Member.Groups = []string{"/org/members"}
	return &r
}

func TemplateNames(t *testing.T, f *Fixture, c *http.Cookie) []string {
	t.Helper()
	w := f.Do("GET", "/api/templates", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("templates %d: %s", w.Code, w.Body)
	}
	var Names []string
	for _, it := range Decode[struct{ Items []struct{ Name string } }](t, w).Items {
		Names = append(Names, it.Name)
	}
	return Names
}

const TemplateBody = `{"displayName":"Lab","kind":"jupyterlab","image":"example.org/lab:1","port":8888}`

func SessionFor(id, display string, groups ...string) session.Session {
	return session.Session{
		Principal: authz.Principal{ID: id, Username: "alice", DisplayName: display, Groups: groups},
		Provider:  "local", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour),
	}
}
