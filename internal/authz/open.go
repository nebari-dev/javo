package authz

import (
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Origin is where app pages are served, as far as opening one goes.
type Origin struct {
	// Shared is true when an app's page shares its origin with the UI (one
	// host) or with the other apps (path mode on an apps host): a page there
	// can act as whoever views it (exposure.SharesOrigin).
	Shared bool
	// OneHost is true when apps are served on the UI host.
	OneHost bool
	// AllowOneHost is config sharing.allowOneHost: shares open on one host.
	AllowOneHost bool
}

// OriginOf is the Origin of apps served by b.
func OriginOf(b exposure.Backend, allowOneHost bool) Origin {
	return Origin{Shared: exposure.SharesOrigin(b), OneHost: !b.SeparateHost(), AllowOneHost: allowOneHost}
}

// OpenOn decides whether p, holding roles, opens obj's app where apps are
// served. d is Decide for ActOpenApp. When d allows, refusal is set (non
// empty) if the app's origin still refuses: while apps share an origin an
// admin opens someone else's app only when it is shared with them, never by
// admin rights alone; a shared app's page can still act as whoever views it
// on the apps host, a limit the docs state (per-app subdomains will lift
// it). On one host, where a page would carry the viewer's UI session, a
// viewer holding any admin role opens only their own apps. Nobody
// opens a preview of a pull
// request, which runs code the owner has not necessarily read (a stranger's,
// for a fork). On one host without allowOneHost only the owner opens: shares
// made before the apps moved there do not.
func OpenOn(p Principal, roles []Role, obj Object, o Origin) (d Decision, refusal reasons.MessageCode) {
	d = Decide(p, roles, ActOpenApp, obj)
	if !d.Allowed || !o.Shared {
		return d, ""
	}
	switch {
	case d.Via == "platform-admin", o.OneHost && d.Via != "owner" && Elevated(roles):
		return d, reasons.MsgAdminOpenNeedsAppsHost
	case obj.App.ParentID != "":
		return d, reasons.MsgPreviewNeedsAppsHost
	case d.Via != "owner" && o.OneHost && !o.AllowOneHost:
		return d, reasons.MsgSharedNeedsAppsHost
	}
	return d, ""
}
