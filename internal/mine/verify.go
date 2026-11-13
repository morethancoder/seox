package mine

import (
	"context"
	"net/url"
	"strings"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/config"
)

// VerifyStep is one thing to do, in order.
type VerifyStep struct {
	N     int      `json:"n"`
	Title string   `json:"title"`
	How   []string `json:"how"`
}

// VerifyResult is the steps to verify a site plus what the signed-in account already has.
type VerifyResult struct {
	Site   string       `json:"site"`
	Steps  []VerifyStep `json:"steps"`
	Google GoogleState  `json:"google"`
	Bing   BingState    `json:"bing"`
	// Next is the one command to run now.
	Next     string   `json:"next"`
	Warnings []string `json:"warnings,omitempty"`
}

// GoogleState says what the signed-in Google account has for the site.
type GoogleState struct {
	SignedIn   bool   `json:"signed_in"`
	Checked    bool   `json:"checked"` // the property list was read
	IsProperty bool   `json:"is_property"`
	Property   string `json:"property,omitempty"`
	Permission string `json:"permission_level,omitempty"`
	Note       string `json:"note,omitempty"`
}

// BingState says what the Bing Webmaster account has for the site.
type BingState struct {
	HasKey     bool   `json:"has_key"`
	Checked    bool   `json:"checked"`
	IsSite     bool   `json:"is_site"`
	SiteURL    string `json:"site_url,omitempty"`
	IsVerified bool   `json:"is_verified,omitempty"`
}

// Verify explains how to verify a site and, where it can, says whether the
// account is already set up. It never fails because a lookup failed: those
// become warnings, since the steps are useful on their own.
func Verify(ctx context.Context, site string) (*VerifyResult, error) {
	host := siteHost(site)
	if host == "" {
		return nil, apperr.New(apperr.InvalidInput, "%q is not a site or domain", site)
	}
	v := &VerifyResult{Site: host, Steps: verifySteps(host)}
	key, _ := config.Key("bing")
	v.Google.SignedIn, v.Bing.HasKey = signedIn(), key != ""

	if v.Google.SignedIn {
		tok, err := accessToken(ctx)
		var sites []Site
		if err == nil {
			sites, err = listSites(ctx, tok)
		}
		if err != nil {
			v.Warnings = append(v.Warnings, "couldn't check Search Console: "+apperr.From(err).Message)
		} else {
			v.Google.Checked = true
			p, un := matchProperty(sites, host)
			switch {
			case p != nil:
				v.Google.IsProperty, v.Google.Property, v.Google.Permission = true, p.URL, p.Permission
				v.Google.Note = "already a property; run `seox mine queries " + host + "`"
			case un != nil:
				v.Google.Property, v.Google.Permission = un.URL, un.Permission
				v.Google.Note = "your account is only an unverified user of this property; finish step 1 and verify"
			default:
				v.Google.Note = "not a property in this account yet; follow step 1"
			}
		}
	}
	if v.Bing.HasKey {
		sites, err := bingSites(ctx, key)
		if err != nil {
			v.Warnings = append(v.Warnings, "couldn't check Bing Webmaster: "+apperr.From(err).Message)
		} else {
			v.Bing.Checked = true
			for _, s := range sites {
				if u, err := url.Parse(s.URL); err == nil && strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.") == host {
					if !v.Bing.IsSite || s.IsVerified {
						v.Bing.IsSite, v.Bing.SiteURL, v.Bing.IsVerified = true, s.URL, s.IsVerified
					}
				}
			}
		}
	}

	switch {
	case !v.Google.SignedIn:
		v.Next = LoginFix
	case v.Google.Checked && !v.Google.IsProperty:
		v.Next = "verify " + host + " in Search Console (step 1), then run `seox setup verify " + host + "` again"
	default:
		v.Next = "seox mine queries " + host
	}
	return v, nil
}

func verifySteps(host string) []VerifyStep {
	return []VerifyStep{
		{1, "Verify the site in Google Search Console (search.google.com/search-console)", []string{
			"Recommended: add a Domain property. Click Add property > Domain, enter " + host + ".",
			"Copy the TXT record Google shows (google-site-verification=...), add it as a TXT record for " + host + " at your DNS host, then click Verify. DNS can take a while to update.",
			"A Domain property covers www, other subdomains, http and https in one view.",
			"Alternative: a URL prefix property. Click Add property > URL prefix, enter https://" + host + "/.",
			"Verify with an HTML file (download googleXXXX.html and upload it to the site root, so https://" + host + "/googleXXXX.html opens) or an HTML tag (paste <meta name=\"google-site-verification\" content=\"...\"> into the <head> of the home page), then click Verify.",
			"Keep the file or tag in place: Google checks it again from time to time.",
		}},
		{2, "Add the site to Bing Webmaster Tools (bing.com/webmasters)", []string{
			"Sign in, choose Import your sites from Google Search Console, and allow access. Bing copies the verified sites.",
			"Or choose Add a site and verify it with an XML file, a meta tag or a DNS CNAME record.",
			"Then open Settings > API access and create an API key.",
		}},
		{3, "Connect seox", []string{
			"Create a Google Cloud OAuth client of type Desktop app, enable the Search Console API, download client_secret.json.",
			"In a terminal run: " + LoginFix,
			"Run: seox key set bing   (reads the Bing key from stdin; optional, Search Console works without it)",
		}},
		{4, "Read your data", []string{"seox mine queries " + host + "   (also: pages, drops, almost). Search Console data lags about two days."}},
	}
}
