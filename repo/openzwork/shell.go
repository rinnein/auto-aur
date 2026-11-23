package openzwork

import "strings"

// pkgbuildScript returns the shell part of the PKGBUILD (helpers, prepare(),
// package()). Placeholders are expanded here so the rest of the generator never
// has to think about quoting the version or the install directory.
func pkgbuildScript() string {
	return strings.NewReplacer(
		"@launcher@", launcher(),
		"@install_dir@", installDir,
	).Replace(shellBody + packageBody)
}

// launcher is launcherScript with the placeholders filled in.
func launcher() string {
	return strings.NewReplacer(
		"@electronversion@", electronMajor,
		"@install_dir@", installDir,
	).Replace(launcherScript)
}

// shellBody is the PKGBUILD's helper functions and prepare(): unpack the
// release, redirect resourcesPath, repack the asar, and create the launcher.
const shellBody = `
_find_app_dir() {
  find "${srcdir}" -type f -name resources.pak -exec dirname {} + | head -n 1
}

prepare() {
  # The release asset is already a pacman package; take its payload and keep
  # the package metadata out of the build tree.
  bsdtar -xf "${srcdir}/${_archive}" -C "${srcdir}" \
    --exclude .PKGINFO --exclude .INSTALL --exclude .MTREE

  local _appdir
  _appdir=$(_find_app_dir)
  if [[ -z "${_appdir}" ]]; then
    error "the release asset did not unpack an Electron application"
    return 1
  fi

  # The bundled runtime is Electron, which the package takes from the
  # repository instead of shipping a second copy. Say so when upstream moves to
  # another series: a newer Electron normally still runs the app, an older one
  # may not.
  local _elec_ver
  _elec_ver=$(strings "${_appdir}/openzwork" | grep -m1 -o 'Electron/[0-9]*\.[0-9]*\.[0-9]*' | cut -d/ -f2)
  if [[ -n "${_elec_ver}" && "${_elec_ver%%.*}" != "${_electronversion}" ]]; then
    warning "openzwork bundles Electron ${_elec_ver}, but the package depends on electron${_electronversion}"
  fi

  # Electron resolves process.resourcesPath to its own resources directory,
  # while the app expects its bundled data (config/, glm/, tools/, icons) next
  # to app.asar. Redirect those lookups to the install location and repack.
  # Native modules stay outside the archive: Electron cannot dlopen them from
  # inside an asar.
  rm -rf "${srcdir}/asar"
  asar e "${_appdir}/resources/app.asar" "${srcdir}/asar"
  find "${srcdir}/asar/out" -type f -exec sed -i "s|process.resourcesPath|'@install_dir@'|g" {} +
  asar p "${srcdir}/asar" "${_appdir}/resources/app.asar" --unpack='*.node'
  rm -rf "${srcdir}/asar"

  # The .desktop file points into the bundled layout; with the system Electron
  # the launcher in /usr/bin is the entry point.
  sed -i "s|/opt/OpenZWork/||g" "${srcdir}/usr/share/applications/openzwork.desktop"

  # Use the ripgrep package instead of the bundled copy.
  ln -sf /usr/bin/rg "${_appdir}/resources/tools/ripgrep/rg"

  # Drop what only belongs to other platforms.
  rm -rf \
    "${_appdir}/resources/apparmor-profile" \
    "${_appdir}/resources/package-type" \
    "${_appdir}/resources/app.asar.unpacked/node_modules/@lydell"
  rm -rf "${_appdir}/resources/app.asar.unpacked/node_modules/node-pty/bin" \
    "${_appdir}/resources/app.asar.unpacked/node_modules/node-pty/deps/winpty"
  rm -rf "${_appdir}/resources/app.asar.unpacked/node_modules/node-pty/prebuilds/darwin-"* \
    "${_appdir}/resources/app.asar.unpacked/node_modules/node-pty/prebuilds/win32-"* \
    "${_appdir}/resources/app.asar.unpacked/node_modules/node-pty/prebuilds/linux-arm64"

  cat > "${srcdir}/openzwork" <<'OPENZWORK_LAUNCHER'
@launcher@OPENZWORK_LAUNCHER
  chmod 755 "${srcdir}/openzwork"
}
`

// packageBody installs the launcher, the application resources, the desktop
// entry and the hicolor icons.
const packageBody = `
package() {
  local _appdir
  _appdir=$(_find_app_dir)

  install -Dm755 "${srcdir}/openzwork" "${pkgdir}/usr/bin/openzwork"

  install -d "${pkgdir}@install_dir@"
  cp -a "${_appdir}/resources/." "${pkgdir}@install_dir@/"

  install -Dm644 "${srcdir}/usr/share/applications/openzwork.desktop" \
    "${pkgdir}/usr/share/applications/openzwork.desktop"

  local _icon _rel
  while IFS= read -r _icon; do
    _rel=${_icon#"${srcdir}/usr/share/icons/"}
    install -Dm644 "${_icon}" "${pkgdir}/usr/share/icons/${_rel}"
  done < <(find "${srcdir}/usr/share/icons" -type f \( -name '*.png' -o -name '*.svg' \))
}
`

// launcherScript starts the app on the system Electron. It is the same shape as
// the zcode-bin launcher: environment defaults, memory-friendly switches,
// per-user extra flags, and an unsandboxed fallback when run as root.
//
// Everything up to and including "${_RUNNAME}" must stay in place: the app
// reads process.argv[1] for deep-link entry, so all switches come after it.
const launcherScript = `#!/bin/bash
set -e

_APPDIR="@install_dir@"
_RUNNAME="${_APPDIR}/app.asar"

export ELECTRON_IS_DEV=0
export ELECTRON_FORCE_IS_PACKAGED=true
export ELECTRON_DISABLE_SECURITY_WARNINGS=true
export NODE_ENV=production
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
export LD_LIBRARY_PATH="${_APPDIR}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

# Let Electron pick X11 or Wayland, and group the window with the .desktop file.
export ELECTRON_OZONE_PLATFORM_HINT="${ELECTRON_OZONE_PLATFORM_HINT:-auto}"
export CHROME_DESKTOP="openzwork.desktop"

# Electron's trash implementation differs per desktop environment.
case "${XDG_CURRENT_DESKTOP}" in
    KDE)   export ELECTRON_TRASH="kioclient5" ;;
    GNOME) export ELECTRON_TRASH="gio" ;;
    XFCE)  export ELECTRON_TRASH="gvfs-trash" ;;
    *)     ;;
esac

# Safe defaults for Electron 44 / Chromium 152: release freed allocator memory
# periodically, throttle background timers, skip a back/forward cache an editor
# never uses, and cap the GPU compositor budget.
_MEMORY_FLAGS=(
    "--enable-features=PartitionAllocMemoryReclaimer,IntensiveWakeUpThrottling"
    "--disable-features=BackForwardCache"
    "--force-gpu-mem-available-mb=512"
)

# Low-RAM machines: ZCODE_MEMORY_SAVER=1 openzwork
if [[ "${ZCODE_MEMORY_SAVER:-0}" == "1" ]]; then
    _MEMORY_FLAGS+=(
        "--disable-gpu"
        "--js-flags=--max-old-space-size=3072"
    )
fi

# Extra switches; later files append to earlier ones:
#   1. system-wide Electron flags
#   2. version-specific Electron flags
#   3. app-wide flags
#   4. app configuration directory flags
_FLAG_SOURCES=(
    "${XDG_CONFIG_HOME}/electron-flags.conf"
    "${XDG_CONFIG_HOME}/electron@electronversion@-flags.conf"
    "${XDG_CONFIG_HOME}/openzwork-flags.conf"
    "${XDG_CONFIG_HOME}/OpenZWork/openzwork-flags.conf"
)

declare -a flags
for _FLAGS_FILE in "${_FLAG_SOURCES[@]}"; do
    if [[ -f "${_FLAGS_FILE}" ]]; then
        echo "Loading flags from ${_FLAGS_FILE}"
        while read -r line || [[ -n "${line}" ]]; do
            [[ "${line}" =~ ^[[:space:]]*#.* ]] || [[ -z "${line}" ]] || {
                read -ra line_flags <<< "${line}"
                flags+=("${line_flags[@]}")
            }
        done < "${_FLAGS_FILE}"
    fi
done

# Chromium's sandbox cannot be used as root.
_SANDBOX_ARG=()
if [[ "${EUID}" -eq 0 ]] && [[ "${ELECTRON_RUN_AS_NODE}" != "1" ]]; then
    _SANDBOX_ARG=("--no-sandbox")
fi

cd "${_APPDIR}"
exec electron@electronversion@ "${_RUNNAME}" "${_MEMORY_FLAGS[@]}" "${flags[@]}" "${_SANDBOX_ARG[@]}" "$@"
`
