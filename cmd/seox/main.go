// Command seox is a free SEO CLI for agents: audits, speed, keyword ideas,
// rankings and authority for any site, plus real search data for sites you verified.
package main

import (
	"os"

	"github.com/morethancoder/seox/internal/cli"
	// Areas attach their handlers to the ops table in init().
	_ "github.com/morethancoder/seox/internal/mine"    // login, logout, Search Console, Bing, setup verify
	_ "github.com/morethancoder/seox/internal/rank"    // SearXNG runner, rank
	_ "github.com/morethancoder/seox/internal/report"  // compare, report
	_ "github.com/morethancoder/seox/internal/signals" // speed, authority, ideas
	_ "github.com/morethancoder/seox/internal/site"    // crawler, audit, page, keywords
)

func main() { os.Exit(cli.Execute()) }
