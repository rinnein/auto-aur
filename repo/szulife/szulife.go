package szulife

import (
	"fmt"
	"strings"

	"github.com/rinnein/auto-aur/internal/conf"
	"github.com/rinnein/auto-aur/internal/pkgbuild"
	"github.com/rinnein/auto-aur/repo"
)

// maintainer is set per run from conf.json.
var maintainer string

func init() {
	repo.Register("your-szu-life-bin", &binGen{})
	repo.Register("your-szu-life-git", &gitGen{})
}

// binGen repackages the prebuilt .deb from the rolling canary release.
type binGen struct{}

func (g *binGen) Pkgname() string { return pkgBin }

func (g *binGen) Wanted() repo.Wanted {
	// The canary tag is reused for every build, so its tag never changes; the
	// version comes from the asset filename and the fingerprint from the
	// release metadata.
	return repo.Wanted{Repo: upstreamRepo, Tag: releaseTag, RepoDescription: true}
}

func (g *binGen) WithMaintainer(m string, _ conf.Overrides) repo.Generator {
	maintainer = m
	return g
}

// debAsset picks the x86_64 .deb and returns its version.
func (g *binGen) debAsset(up repo.Upstream) (repo.Asset, string, error) {
	for _, a := range up.Assets {
		if !strings.HasSuffix(a.Name, ".deb") {
			continue
		}
		if !strings.Contains(a.Name, "linux_x64") {
			continue
		}
		ver, ok := versionFromAsset(a.Name)
		if !ok {
			return repo.Asset{}, "", fmt.Errorf("szulife: cannot read a version from asset %q", a.Name)
		}
		return a, ver, nil
	}
	return repo.Asset{}, "", fmt.Errorf("szulife: release %s has no linux_x64 .deb asset", up.Tag)
}

func (g *binGen) Pkgver(up repo.Upstream) (string, error) {
	_, ver, err := g.debAsset(up)
	return ver, err
}

func (g *binGen) Render(ctx repo.Context) (repo.Generated, error) {
	asset, ver, err := g.debAsset(ctx.Upstream)
	if err != nil {
		return repo.Generated{}, err
	}
	if ctx.Net.Digest == nil {
		return repo.Generated{}, fmt.Errorf("szulife: no digest helper configured")
	}
	// The release ships no checksum file, so the .deb is streamed once to
	// compute its sha256. This is required for a trustworthy -bin package.
	sum, err := ctx.Net.Digest(asset.URL, "sha256")
	if err != nil {
		return repo.Generated{}, fmt.Errorf("szulife: checksum %s: %w", asset.Name, err)
	}
	if len(sum) != 64 {
		return repo.Generated{}, fmt.Errorf("szulife: unexpected sha256 %q for %s", sum, asset.Name)
	}

	pkgrel := ctx.Pkgrel
	if pkgrel == 0 {
		pkgrel = 1
	}

	// pkgdesc is the upstream About text, resolved per run.
	desc, note := chooseDesc(ctx.Upstream)
	var b strings.Builder
	b.WriteString(commonHeader(pkgBin, ver, pkgrel, desc))
	// A prebuilt binary must not be stripped, and debug splitting would only
	// add an empty usr/src/debug tree.
	b.WriteString("options=(" + quoteAll(binOptions) + ")\n")
	// The filename is embedded so the source is reproducible and the version is
	// visible in the download.
	fmt.Fprintf(&b, "\n_debname=\"%s\"\n", asset.Name)
	fmt.Fprintf(&b, "source_x86_64=(\"${_debname}::%s\")\n", asset.URL)
	fmt.Fprintf(&b, "sha256sums_x86_64=('%s')\n", sum)
	b.WriteString(`
package() {
  cd "${srcdir}"
  # A .deb is an ar archive holding control.tar.* and data.tar.*. makepkg does
  # not unpack it, so the payload is extracted explicitly and installed as-is.
  local inner
  inner=$(bsdtar -tf "${_debname}" | grep -E '^data\.tar\.')
  if [[ -z "${inner}" ]]; then
    error "no data.tar.* inside ${_debname}"
    return 1
  fi
  bsdtar -xf "${_debname}" "${inner}"
  bsdtar -xf "${inner}" -C "${pkgdir}"
` + dropBundledAria2 + `}
`)

	src := b.String()
	return repo.Generated{
		Pkgname:     pkgBin,
		Pkgver:      ver,
		Pkgrel:      pkgrel,
		PKGBUILD:    src,
		Srcinfo:     srcinfo(pkgBin, desc, ver, pkgrel, []extraFields{{Key: "options", Values: binOptions}}, []string{asset.Name + "::" + asset.URL}, []string{sum}, "x86_64"),
		Fields:      pkgbuild.Parse(src),
		Fingerprint: ctx.Upstream.Fingerprint,
		Note:        note,
	}, nil
}

// gitGen builds from the main branch.
type gitGen struct{}

func (g *gitGen) Pkgname() string { return pkgGit }

func (g *gitGen) Wanted() repo.Wanted {
	return repo.Wanted{Repo: upstreamRepo, Branch: "main", RepoDescription: true}
}

func (g *gitGen) WithMaintainer(m string, _ conf.Overrides) repo.Generator {
	maintainer = m
	return g
}

// gitPkgver mirrors the pkgver() function: <UTC commit date>.r<short sha>.
func gitPkgver(up repo.Upstream) (string, error) {
	if up.Commit == "" {
		return "", fmt.Errorf("szulife: branch head has no commit")
	}
	date := up.Date
	if date == "" {
		return "", fmt.Errorf("szulife: branch head has no commit date")
	}
	short := up.Commit
	if len(short) > 7 {
		short = short[:7]
	}
	return fmt.Sprintf("%s.r%s", date, short), nil
}

func (g *gitGen) Pkgver(up repo.Upstream) (string, error) { return gitPkgver(up) }

func (g *gitGen) Render(ctx repo.Context) (repo.Generated, error) {
	ver, err := gitPkgver(ctx.Upstream)
	if err != nil {
		return repo.Generated{}, err
	}
	pkgrel := ctx.Pkgrel
	if pkgrel == 0 {
		pkgrel = 1
	}

	desc, note := chooseDesc(ctx.Upstream)
	var b strings.Builder
	b.WriteString(commonHeader(pkgGit, ver, pkgrel, desc))
	b.WriteString("makedepends=(" + quoteAll(gitMakedepends) + ")\n")
	fmt.Fprintf(&b, "source=(\"$pkgname::git+https://github.com/%s.git#branch=main\")\n", upstreamRepo)
	b.WriteString("sha256sums=('SKIP')\n")
	b.WriteString(`
pkgver() {
  cd "$srcdir/$pkgname"
  # Must agree with the version auto-aur publishes: UTC date + short sha.
  printf '%s.r%s\n' \
    "$(TZ=UTC git log -1 --date=format-local:%Y%m%d --format=%cd)" \
    "$(git rev-parse --short=7 HEAD)"
}

build() {
  cd "$srcdir/$pkgname"
  pnpm install --frozen-lockfile
  pnpm tauri build --ci --bundles deb

  local debs=(src-tauri/target/release/bundle/deb/*.deb)
  if (( ${#debs[@]} != 1 )); then
    error "expected one Debian package, found ${#debs[@]}"
    return 1
  fi
  # A .deb is an ar archive holding control.tar.* and data.tar.*; the payload
  # has to be unpacked in two steps (bsdtar -O cannot stream a named member).
  rm -rf "$srcdir/package-root"
  mkdir -p "$srcdir/package-root"
  cd "$srcdir"
  local inner
  inner=$(bsdtar -tf "${debs[0]}" | grep -E '^data\.tar\.')
  if [[ -z "${inner}" ]]; then
    error "no data.tar.* inside ${debs[0]}"
    return 1
  fi
  bsdtar -xf "${debs[0]}" "${inner}"
  bsdtar -xf "${inner}" -C "$srcdir/package-root"
  rm -f "${inner}"
}

package() {
  cp -a "$srcdir/package-root/usr" "$pkgdir/"
` + dropBundledAria2 + `}
`)

	src := b.String()
	return repo.Generated{
		Pkgname:  pkgGit,
		Pkgver:   ver,
		Pkgrel:   pkgrel,
		PKGBUILD: src,
		Srcinfo: srcinfo(pkgGit, desc, ver, pkgrel,
			[]extraFields{{Key: "makedepends", Values: gitMakedepends}},
			[]string{pkgGit + "::git+https://github.com/" + upstreamRepo + ".git#branch=main"},
			[]string{"SKIP"}, ""),
		Fields:      pkgbuild.Parse(src),
		Fingerprint: ctx.Upstream.Fingerprint,
		Note:        note,
	}, nil
}
