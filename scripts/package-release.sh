#!/usr/bin/env bash
set -euo pipefail

VERSION="${VERSION:?VERSION is required}"
FRP_VERSION="${FRP_VERSION:?FRP_VERSION is required}"
DIST_DIR="${DIST_DIR:-dist}"
RELEASE_DIR="${RELEASE_DIR:-release}"
RELEASE_DIR_ABS="$(mkdir -p "${RELEASE_DIR}" && cd "${RELEASE_DIR}" && pwd)"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

download_frp_archive() {
  local os="$1"
  local arch="$2"
  local base="frp_${FRP_VERSION#v}_${os}_${arch}"
  local tarball="${tmp_dir}/${base}.tar.gz"
  local zipball="${tmp_dir}/${base}.zip"
  local tar_url="https://github.com/fatedier/frp/releases/download/${FRP_VERSION}/${base}.tar.gz"
  local zip_url="https://github.com/fatedier/frp/releases/download/${FRP_VERSION}/${base}.zip"

  if curl -fsSL -o "${tarball}" "${tar_url}"; then
    printf '%s\n' "${tarball}"
    return 0
  fi

  curl -fsSL -o "${zipball}" "${zip_url}"
  printf '%s\n' "${zipball}"
}

extract_frp_archive() {
  local archive="$1"
  local outdir="$2"

  mkdir -p "${outdir}"
  case "${archive}" in
    *.tar.gz)
      tar -xzf "${archive}" -C "${outdir}"
      ;;
    *.zip)
      unzip -q "${archive}" -d "${outdir}"
      ;;
    *)
      echo "unsupported archive format: ${archive}" >&2
      return 1
      ;;
  esac
}

copy_frp_binaries() {
  local os="$1"
  local bundle_dir="$2"
  local extract_dir="$3"
  local frpc_name="frpc"
  local frps_name="frps"

  if [[ "${os}" == "windows" ]]; then
    frpc_name="frpc.exe"
    frps_name="frps.exe"
  fi

  local root
  root="$(find "${extract_dir}" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
  if [[ -z "${root}" ]]; then
    echo "unable to find extracted frp directory in ${extract_dir}" >&2
    return 1
  fi

  cp "${root}/${frpc_name}" "${bundle_dir}/bin/${frpc_name}"
  cp "${root}/${frps_name}" "${bundle_dir}/bin/${frps_name}"

  if [[ ! -f "${root}/LICENSE" ]]; then
    echo "missing LICENSE in upstream frp archive: ${root}" >&2
    return 1
  fi
  cp "${root}/LICENSE" "${bundle_dir}/licenses/frp/LICENSE"
}

write_bundle_metadata() {
  local bundle_dir="$1"

  cat > "${bundle_dir}/BUNDLED_COMPONENTS.txt" <<EOF
Orion release bundle

- orion: ${VERSION}
- frp: ${FRP_VERSION}

frp upstream:
https://github.com/fatedier/frp
https://github.com/fatedier/frp/releases/tag/${FRP_VERSION}
EOF
}

package_bundle() {
  local platform="$1"
  local frp_platform="$2"
  local archive_ext="$3"
  local orion_binary="$4"
  local orion_name="$5"

  local bundle_name="orion-${platform}-${VERSION}"
  local bundle_dir="${tmp_dir}/${bundle_name}"
  local extract_dir="${tmp_dir}/extract-${platform}"

  mkdir -p "${bundle_dir}/bin" "${bundle_dir}/licenses/frp"
  cp "${DIST_DIR}/${orion_binary}" "${bundle_dir}/bin/${orion_name}"
  cp README.md "${bundle_dir}/README.md"
  cp THIRD_PARTY_NOTICES.md "${bundle_dir}/THIRD_PARTY_NOTICES.md"

  local archive
  archive="$(download_frp_archive "${frp_platform%_*}" "${frp_platform#*_}")"
  extract_frp_archive "${archive}" "${extract_dir}"
  copy_frp_binaries "${frp_platform%_*}" "${bundle_dir}" "${extract_dir}"
  write_bundle_metadata "${bundle_dir}"

  case "${archive_ext}" in
    tar.gz)
      tar -C "${tmp_dir}" -czf "${RELEASE_DIR_ABS}/${bundle_name}.tar.gz" "${bundle_name}"
      ;;
    zip)
      (
        cd "${tmp_dir}"
        zip -qr "${RELEASE_DIR_ABS}/${bundle_name}.zip" "${bundle_name}"
      )
      ;;
    *)
      echo "unsupported package format: ${archive_ext}" >&2
      return 1
      ;;
  esac
}

package_bundle "linux-amd64" "linux_amd64" "tar.gz" "orion-linux-amd64" "orion"
package_bundle "linux-arm64" "linux_arm64" "tar.gz" "orion-linux-arm64" "orion"
package_bundle "darwin-amd64" "darwin_amd64" "tar.gz" "orion-darwin-amd64" "orion"
package_bundle "darwin-arm64" "darwin_arm64" "tar.gz" "orion-darwin-arm64" "orion"
package_bundle "windows-amd64" "windows_amd64" "zip" "orion-windows-amd64.exe" "orion.exe"

sha256sum "${RELEASE_DIR_ABS}"/* > "${RELEASE_DIR_ABS}/checksums.txt"
