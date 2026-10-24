package animeko

import "github.com/rinnein/auto-aur/repo"

// Beta tracks the newest release whose name or tag contains "beta".
func init() {
	repo.Register("animeko-beta", New(Channel{
		Task:    "animeko-beta",
		Pkgname: "animeko-appimage-beta",
		Search:  "beta",
		Disname: "Animeko Beta",
		Depends: previewDeps,
		IconDir: "512x512",
	}))
}
