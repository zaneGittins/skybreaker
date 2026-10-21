package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Misnamed says what to do when ui_url names another machine than self,
// this one's name on the tailnet: Tailscale gives a second machine asking
// for a taken name one of its own, so a second Skybreaker on a tailnet
// answers somewhere else than its config says. Links, previews and the
// GitHub App would then point at the other machine. "" when they agree or
// either is unknown.
func Misnamed(uiURL, self string) string {
	u, err := url.Parse(uiURL)
	if err != nil || u.Hostname() == "" || self == "" || strings.EqualFold(u.Hostname(), self) {
		return ""
	}
	return fmt.Sprintf("ui_url names %s, but this machine is %s on your tailnet: another machine there has the name. "+
		"Set tailscale.hostname and ui_url in the config file to match (https://%s), or rename the other machine, then restart Skybreaker",
		u.Hostname(), self, self)
}
