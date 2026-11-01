# Template: scripts/gen-spec.sh ('make gen-spec') fills in the version, the
# commit and the changelog. Do not build this file directly.
%global units node-tracker.service node-tracker-once.service node-tracker-once.timer node-tracker-report.service node-tracker-report.timer
# Offline build with the vendored modules and the distro's Go. No -trimpath:
# the debugsource package needs the source paths.
%global goenv GOFLAGS=-mod=vendor GOPROXY=off GOTOOLCHAIN=local CGO_ENABLED=0

Name:           node-tracker
Version:        @VERSION@
Release:        1%{?dist}
Summary:        Alert when a Bitcoin node falls behind the chain tip

License:        GPL-3.0-only
URL:            https://github.com/al-maisan/node-tracker
# The tagged tree plus the vendored modules, from scripts/src-tarball.sh.
Source0:        node-tracker-%{version}.tar.gz
# From the checkout (scripts/srpm.sh), so they also work for older tags.
Source1:        install-units.sh
Source2:        node-tracker.sysusers

BuildRequires:  golang >= 1.26
BuildRequires:  systemd-rpm-macros

%description
node-tracker compares a Bitcoin node's block height with independent views
of the chain (mempool.space, blockstream.info or other nodes) and sends a
Telegram message when the node falls behind, and another when it catches up.
Alerts are debounced and queued until they are delivered.

It runs as a daemon, from a systemd timer or cron, or by hand. The package
ships systemd system and user units and creates the node-tracker system
user; no unit is enabled or started.

%prep
%autosetup
# The binary links go-yaml (MIT and Apache-2.0).
cp -p vendor/go.yaml.in/yaml/v3/LICENSE LICENSE.go-yaml
cp -p vendor/go.yaml.in/yaml/v3/NOTICE NOTICE.go-yaml

%build
# Build time = changelog date, so a rebuild gives the same binary.
build_time=$(date -u -d "@${SOURCE_DATE_EPOCH:?}" +%%Y-%%m-%%dT%%H:%%M:%%SZ)
%{goenv} go build -ldflags "-X main.version=v%{version} -X main.commit=@COMMIT@ -X main.buildTime=$build_time" \
    -o bin/node-tracker ./cmd/node-tracker

%check
# The integration tests need bitcoind and are not built without their tag.
%{goenv} go test ./...

%install
install -Dpm 0755 bin/node-tracker %{buildroot}%{_bindir}/node-tracker
install -Dpm 0644 -t %{buildroot}%{_docdir}/%{name} README.md
install -Dpm 0644 -t %{buildroot}%{_docdir}/%{name}/assets assets/*
install -Dpm 0644 -t %{buildroot}%{_docdir}/%{name}/docs docs/*.md
install -Dpm 0644 -t %{buildroot}%{_docdir}/%{name}/examples examples/config.yaml
install -Dpm 0644 %{SOURCE2} %{buildroot}%{_sysusersdir}/node-tracker.conf
bash %{SOURCE1} deploy/systemd %{buildroot}%{_unitdir} %{buildroot}%{_userunitdir}

# Presets, not enable: Fedora's default preset leaves the units disabled.
%post
%systemd_post %{units}
%systemd_user_post %{units}

%preun
%systemd_preun %{units}
%systemd_user_preun %{units}

%postun
%systemd_postun %{units}
%systemd_user_postun %{units}

%files
%license LICENSE LICENSE.go-yaml NOTICE.go-yaml
%{_bindir}/node-tracker
%{_docdir}/%{name}/
%{_sysusersdir}/node-tracker.conf
%{_unitdir}/node-tracker*
%{_userunitdir}/node-tracker*

%changelog
@CHANGELOG@
