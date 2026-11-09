package web

import (
	"net/http"
	"strings"

	"ffnvr/internal/profile"
)

// The look of the panel: day (the default), night, or automatic (as the system of the
// browser has it, prefers-color-scheme). The stylesheets name their light surfaces,
// text and lines by the variables below (--c-*); a theme only gives them other values.
// What is dark in both (the header, the video tiles, the player, the timeline, the
// log) keeps its own colors.
//
// <html data-theme="day|night|auto"> picks the theme; the users' choice is in their
// profile (profile.Settings.Theme), the sign in page follows the cookie themeCookie,
// set whenever a signed in page is shown.

// the day: the colors of the mockups
const dayVars = `color-scheme:light;
--c-bg:#f6f8fb;--c-sub:#f6f8fb;--c-card:#fff;--c-tabsel:#fff;
--c-fg:#182433;--c-muted:#5b6878;--c-muted2:#44505f;--c-faint:#aab4c0;--c-link:#0559a8;--c-link-h:#033f78;
--c-hover:#f1f4f8;--c-sel:#e6f0fb;--c-sel2:#eef5fd;--c-rowhover:#f5f8fc;
--c-fill:#e8ebef;--c-fill2:#eef1f4;--c-fill3:#e2e6eb;--c-idle:#d5dbe2;--c-mapbg:#e9edf1;
--c-line:#dce1e7;--c-line2:#e3e7ec;--c-line3:#e8ebef;--c-line4:#eef1f4;--c-input-line:#ccd3dc;--c-line-dash:#c3ccd6;
--c-ok-bg:#d7f0dc;--c-ok-bg2:#e9f7ec;--c-ok-fg:#1a6b2c;--c-ok-line:#b9e2c2;
--c-warn-bg:#fdebd0;--c-warn-bg2:#fff4d6;--c-warn-fg:#8a4b08;
--c-err-bg:#f8dadb;--c-err-bg2:#fdf1f1;--c-err-fg:#a3222b;--c-err-fg2:#7f1d22;--c-err-line:#e7b4b8;--c-err-line2:#f1c4c6;
--c-info-bg:#eaf2fb;--c-info-fg:#0b4a87;--c-info-line:#c3daf3;
--c-hdr-line:transparent;--c-mapfilter:none`

// the night: dark surfaces, light text, the accents softened so that they do not glare
const nightVars = `color-scheme:dark;
--c-bg:#10161f;--c-sub:#19222e;--c-card:#161e29;--c-tabsel:#33435a;
--c-fg:#d9e1ea;--c-muted:#909fb1;--c-muted2:#a7b3c2;--c-faint:#5b697a;--c-link:#6aaef0;--c-link-h:#97c6f5;
--c-hover:#212c3a;--c-sel:#17304f;--c-sel2:#152841;--c-rowhover:#1c2633;
--c-fill:#263243;--c-fill2:#212c3a;--c-fill3:#2b3748;--c-idle:#334052;--c-mapbg:#1b2430;
--c-line:#2b3747;--c-line2:#273342;--c-line3:#253140;--c-line4:#212b38;--c-input-line:#3a4859;--c-line-dash:#3d4b5d;
--c-ok-bg:#173a23;--c-ok-bg2:#132a1b;--c-ok-fg:#7dd491;--c-ok-line:#2a5a37;
--c-warn-bg:#3b2b10;--c-warn-bg2:#3a3012;--c-warn-fg:#f2b45c;
--c-err-bg:#43191f;--c-err-bg2:#2e171b;--c-err-fg:#f3959b;--c-err-fg2:#f5abb0;--c-err-line:#6c3239;--c-err-line2:#5e2a31;
--c-info-bg:#13253b;--c-info-fg:#9cc8f3;--c-info-line:#23466e;
--c-hdr-line:#24324a;--c-mapfilter:brightness(.72) contrast(1.08) saturate(.85)`

// themeCSS opens both stylesheets: the day unless the page says otherwise
const themeCSS = `
:root{` + dayVars + `}
html[data-theme=night]{` + nightVars + `}
@media (prefers-color-scheme:dark){html[data-theme=auto]{` + nightVars + `}}
`

// themeCookie: the theme of the last signed in page, for the sign in page (no user yet)
const themeCookie = "ffnvr_theme"

// colorScheme: the content of <meta name="color-scheme"> for a theme (the browser's own
// controls, scrollbars and the page before the stylesheet)
func colorScheme(theme string) string {
	switch theme {
	case profile.ThemeNight:
		return "dark"
	case profile.ThemeAuto:
		return "light dark"
	}
	return "light"
}

// cookieTheme: the theme of the cookie; none or unknown: the day
func cookieTheme(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil {
		return profile.NormTheme(strings.TrimSpace(c.Value))
	}
	return profile.ThemeDay
}

// rememberTheme keeps the user's theme in the cookie when it differs (a year)
func rememberTheme(w http.ResponseWriter, r *http.Request, theme string) {
	if c, err := r.Cookie(themeCookie); err == nil && c.Value == theme {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: theme, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode, Secure: secure(r)})
}
