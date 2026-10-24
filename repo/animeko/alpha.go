package animeko

import "github.com/rinnein/auto-aur/repo"

// Alpha tracks the newest release whose name or tag contains "alpha".
func init() {
	repo.Register("animeko-alpha", New(Channel{
		Task:    "animeko-alpha",
		Pkgname: "animeko-appimage-alpha",
		Search:  "alpha",
		Disname: "Animeko Alpha",
		Depends: previewDeps,
		IconDir: "512x512",
	}))
}
