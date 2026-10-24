package animeko

import "github.com/rinnein/auto-aur/repo"

// Stable is the main release channel: no search term, plain "latest" release.
func init() {
	repo.Register("animeko", New(Channel{
		Task:    "animeko",
		Pkgname: "animeko-appimage",
		// Tag empty + Search empty => /releases/latest
		Disname: "",
		Depends: stableDeps,
		IconDir: "128x128",
	}))
}
