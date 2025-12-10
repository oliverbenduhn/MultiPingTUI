#!/bin/bash

MODULE=$(grep module go.mod | cut -d\  -f2)
BINBASE="mping"
VERSION=${VERSION:-$GITHUB_REF_NAME}
# Fallback: read version from main.go to keep single source of truth
if [ -z "$VERSION" ]; then
  VERSION=$(grep -E '^var Version = "v[0-9]+\.[0-9]+\.[0-9]+"' main.go | head -n1 | sed -E 's/.*"([^"]+)".*/\1/')
fi
# Last resort
VERSION=${VERSION:-v0.0.0}
PKG_VERSION=${VERSION#v}
MAINTAINER=${MAINTAINER:-"oliverbenduhn"}
COMMIT_HASH="$(git rev-parse --short HEAD 2>/dev/null)"
COMMIT_HASH=${COMMIT_HASH:-00000000}
DIRTY=$(git diff --quiet 2>/dev/null || echo '-dirty')
BUILD_TIMESTAMP=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
BUILDER=$(go version)
HOST_GOOS=$(go env GOOS)
HOST_GOARCH=$(go env GOARCH)

[ -d dist ] && rm -rf dist
mkdir dist

# For version in sub module
# "-X '${MODULE}/main.Version=${VERSION}'"

LDFLAGS=(
  "-X 'main.Version=${VERSION}'"
  "-X 'main.CommitHash=${COMMIT_HASH}${DIRTY}'"
  "-X 'main.BuildTimestamp=${BUILD_TIMESTAMP}'"
  "-X 'main.Builder=${BUILDER}'"
)
echo "[*] Build info"
echo  "   Version=${VERSION}"
echo  "   CommitHash=${COMMIT_HASH}${DIRTY}"
echo  "   BuildTimestamp=${BUILD_TIMESTAMP}"
echo  "   Builder=${BUILDER}"

#echo "[*] go get"
#go get .

echo "[*] go builds:"
TARGETS="linux/amd64 windows/amd64"
#set -x
for DIST in $TARGETS; do
  GOOS=${DIST%/*}
  GOARCH=${DIST#*/}
  echo "[+]   $DIST:"
  echo "[-]    - build"
  SUFFIX=""
  [ "$GOOS" = "windows" ] && SUFFIX=".exe"
  TARGET=${BINBASE}-${GOOS}-${GOARCH}
  env CGO_ENABLED=0 GOOS=$GOOS GOARCH=$GOARCH go build -ldflags="${LDFLAGS[*]}" -mod vendor -o dist/${TARGET}${SUFFIX}
  if [ "$GOOS" = "linux" ] && [ "$GOARCH" = "amd64" ]; then
    if command -v dpkg-deb >/dev/null 2>&1; then
      echo "[-]    - build .deb"
      PKG_STAGING="dist/deb-staging"
      rm -rf "${PKG_STAGING}"
      mkdir -p "${PKG_STAGING}/DEBIAN" "${PKG_STAGING}/usr/bin"
      cp dist/${TARGET}${SUFFIX} "${PKG_STAGING}/usr/bin/${BINBASE}${SUFFIX}"
      cat > "${PKG_STAGING}/DEBIAN/control" <<EOF
Package: ${BINBASE}
Version: ${PKG_VERSION}
Section: net
Priority: optional
Architecture: amd64
Maintainer: ${MAINTAINER}
Description: MultiPingTUI CLI (${BINBASE}) - multi-host ping/TCP probe TUI
EOF
      dpkg-deb --build "${PKG_STAGING}" "dist/${BINBASE}_${PKG_VERSION}_amd64.deb"
      rm -rf "${PKG_STAGING}"
    else
      echo "[-]    - dpkg-deb not found, skipping .deb creation"
    fi

    echo "[-]    - stage Arch package"
    ARCH_STAGING="dist/arch"
    rm -rf "${ARCH_STAGING}"
    mkdir -p "${ARCH_STAGING}"
    cp dist/${TARGET}${SUFFIX} "${ARCH_STAGING}/${BINBASE}"

    # Create PKGBUILD
    cat > "${ARCH_STAGING}/PKGBUILD" <<EOF
pkgname=${BINBASE}
pkgver=${PKG_VERSION}
pkgrel=1
pkgdesc="MultiPingTUI CLI - multi-host ping/TCP probe TUI"
arch=('x86_64')
url="https://github.com/${MAINTAINER}/MultiPingTUI"
license=('MIT')
depends=('glibc')
source=("${BINBASE}")
md5sums=('SKIP')

package() {
	install -Dm755 "\${srcdir}/${BINBASE}" "\${pkgdir}/usr/bin/${BINBASE}"
}
EOF
    if command -v makepkg >/dev/null 2>&1; then
       echo "[-]    - build Arch package"
       (cd "${ARCH_STAGING}" && makepkg -f)
       mv "${ARCH_STAGING}"/*.pkg.tar.zst dist/
       rm -rf "${ARCH_STAGING}"
    else
       echo "[-]    - makepkg not found, Arch PKGBUILD staged in dist/arch for manual build"
    fi
  fi
  (cd dist; sha256sum ${TARGET}${SUFFIX}) | tee -a ${BINBASE}.sha256sum
  if [ -z "$NOCOMPRESS" ]; then
    echo "[-]    - compress"
    if [ "$GOOS" = "windows" ]; then
      xz --keep dist/${TARGET}${SUFFIX}
      if command -v zip >/dev/null 2>&1; then
        (cd dist; zip -qm9 ${TARGET}.zip ${TARGET}${SUFFIX})
      else
        echo "[-]    - zip not found, skipping zip creation"
      fi
    else
      xz dist/${TARGET}
    fi
  fi
done

echo "[*] sha256sum"
(cd dist; sha256sum *) | tee -a ${BINBASE}.sha256sum
mv ${BINBASE}.sha256sum dist/

#echo "[*] pack"
#tar -cvf all.tar -C dist/ . && mv all.tar dist

if [ -z "$SKIP_GH_UPLOAD" ]; then
  if command -v gh >/dev/null 2>&1; then
    echo "[*] GitHub upload"
    REMOTE_URL=$(git config --get remote.origin.url 2>/dev/null || echo "")
    if [[ -z "$GH_REPO" && "$REMOTE_URL" =~ github.com[:/]+([^/]+/[^/.]+) ]]; then
      GH_REPO="${BASH_REMATCH[1]}"
    fi
    GH_REPO=${GH_REPO:-"oliverbenduhn/MultiPingTUI"}
    echo "[-]    - target repo: ${GH_REPO}"

    mapfile -t DIST_FILES < <(find dist -maxdepth 1 -type f | sort)
    if [ ${#DIST_FILES[@]} -eq 0 ]; then
      echo "[-]    - no dist files found, skipping upload"
    else
      if gh release view "${VERSION}" --repo "${GH_REPO}" >/dev/null 2>&1; then
        echo "[-]    - release ${VERSION} exists, uploading assets"
        gh release upload "${VERSION}" "${DIST_FILES[@]}" --clobber --repo "${GH_REPO}"
      else
        echo "[-]    - create release ${VERSION} and upload assets"
        gh release create "${VERSION}" "${DIST_FILES[@]}" --title "${VERSION}" --notes "Automated release for ${VERSION}" --repo "${GH_REPO}"
      fi
    fi
  else
    echo "[!] gh not found, skipping GitHub upload (set SKIP_GH_UPLOAD=1 to silence)"
  fi
fi

echo "[*] done"
