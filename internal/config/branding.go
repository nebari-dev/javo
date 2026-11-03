package config

import (
	"net/url"
	"path/filepath"
	"regexp"
	"unicode"
	"unicode/utf8"
)

// DefaultBrandName is the product name the UI shows when branding.name is
// unset. It is display text only: object names, labels, cookies and env vars
// keep model.ProductName.
const DefaultBrandName = "Javo"

// MaxBrandNameRunes caps branding.name, so it fits the sidebar and a tab title.
const MaxBrandNameRunes = 64

// BrandingCfg is the install's white-label look, shown to everyone,
// signed in or not.
type BrandingCfg struct {
	// Name is shown in the sidebar, the sign-in page and the page title.
	Name string `json:"name,omitempty"`
	// Accent is a #rgb or #rrggbb colour for the accent of both themes;
	// empty keeps the built-in accents.
	Accent string `json:"accent,omitempty"`
	// Logo is the mark at the top left of the sidebar and on the sign-in
	// page; unset keeps the Javo mark.
	Logo struct {
		Light BrandImage `json:"light"`
		// Dark is used in the dark theme; unset uses Light there too.
		Dark BrandImage `json:"dark"`
	} `json:"logo"`
	// Favicon is the browser tab's icon; unset keeps the Javo icon.
	Favicon BrandImage `json:"favicon"`
}

// BrandImage is one image: an https URL the browser loads, or a file the
// gateway serves (the chart mounts it from a ConfigMap). At most one is set.
type BrandImage struct {
	URL  string `json:"url,omitempty"`
	File string `json:"file,omitempty"`
}

// Set reports whether the image is configured.
func (i BrandImage) Set() bool { return i.URL != "" || i.File != "" }

var hexColour = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func (c *BrandingCfg) applyDefaults() { setStr(&c.Name, DefaultBrandName) }

func (c BrandingCfg) validate(bad func(key, format string, a ...any)) {
	if n := utf8.RuneCountInString(c.Name); n > MaxBrandNameRunes {
		bad("branding.name", "%d characters is over %d", n, MaxBrandNameRunes)
	}
	for _, r := range c.Name {
		if unicode.IsControl(r) {
			bad("branding.name", "must not hold control characters such as a new line")
			break
		}
	}
	if c.Accent != "" && !hexColour.MatchString(c.Accent) {
		bad("branding.accent", "%q is not a #rgb or #rrggbb colour", c.Accent)
	}
	c.Logo.Light.validate("branding.logo.light", bad)
	c.Logo.Dark.validate("branding.logo.dark", bad)
	c.Favicon.validate("branding.favicon", bad)
	if c.Logo.Dark.Set() && !c.Logo.Light.Set() {
		bad("branding.logo.dark", "needs branding.logo.light: the light theme would show the Javo mark")
	}
}

func (i BrandImage) validate(key string, bad func(key, format string, a ...any)) {
	if i.URL != "" && i.File != "" {
		bad(key, "set url or file, not both")
		return
	}
	if i.URL != "" {
		u, err := url.Parse(i.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			bad(key+".url", "must be an https address with a host and no user name or password")
		}
	}
	if i.File != "" && !filepath.IsAbs(i.File) {
		bad(key+".file", "%q is not an absolute path", i.File)
	}
}
