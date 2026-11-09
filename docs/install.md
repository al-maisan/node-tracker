# Installing node-tracker

The easiest way first. Then run `node-tracker setup`, see the
[Quick start](../README.md#quick-start): as yourself if bitcoind runs as
you, with `sudo` if it is a system service
([System or user service](operations.md#system-or-user-service)).

## Ubuntu 26.04: PPA

amd64, arm64, from [`ppa:al-maisan/node-tracker`](https://launchpad.net/~al-maisan/+archive/ubuntu/node-tracker):

```sh
sudo add-apt-repository ppa:al-maisan/node-tracker && sudo apt install node-tracker
```

## Fedora: COPR

x86_64, aarch64, from [`al-maisan/node-tracker`](https://copr.fedorainfracloud.org/coprs/al-maisan/node-tracker/):

```sh
sudo dnf copr enable al-maisan/node-tracker && sudo dnf install node-tracker
```

Both packages install `/usr/bin/node-tracker` with its man page, the
systemd units (none is enabled) and a `node-tracker` system user, and are
updated with the system (`apt upgrade`, `dnf upgrade`). The docs and the
example config are in `/usr/share/doc/node-tracker`; for the service, see
[Operations](operations.md#from-a-package).

## Debian, Raspberry Pi OS, other Ubuntu releases: `.deb`

amd64, arm64, armhf; from v1.1.0 on. Download the `.deb` for your system
from the [releases page](https://github.com/al-maisan/node-tracker/releases)
together with `SHA256SUMS` and `SHA256SUMS.asc` into one directory,
[verify it](#verify-a-download), then install it (example: v1.4.0 on a
64-bit Raspberry Pi OS):

```sh
sudo apt install ./node-tracker_1.4.0-1_arm64.deb
```

A note that the download "is performed unsandboxed as root" is harmless:
your home directory is private, so apt reads the file as root.

It installs the same as the PPA package, but updates are by hand: install
the next release's `.deb` the same way.

## macOS and Linux: Homebrew

macOS, Linux amd64/arm64:

```sh
brew install al-maisan/tap/node-tracker
```

The docs are in `$(brew --prefix)/share/doc/node-tracker`, the systemd
units and the example config in `$(brew --prefix)/share/node-tracker`, and
`man node-tracker` works.

To run it in the background (launchd on macOS, a systemd user service on
Linux), write the config with `node-tracker setup` (or put it in
`~/.config/node-tracker/config.yaml`), then:

```sh
node-tracker check
brew services start node-tracker
```

Start it once `check` ends with `config ok`; a node that is behind (exit
code 4) is what the service alerts about. It logs to
`$(brew --prefix)/var/log/node-tracker.log`. On Linux, also run
`loginctl enable-linger` to keep it running after you log out.

## Any Linux or macOS: release archive

Linux amd64, arm64, armv6 (a 32-bit OS on any Raspberry Pi; armv7 up to
v1.0.0); macOS amd64, arm64. Download the archive for your system from the
[releases page](https://github.com/al-maisan/node-tracker/releases)
together with `SHA256SUMS` and `SHA256SUMS.asc` into one directory,
[verify it](#verify-a-download), then unpack it and put the binary on your
`PATH` (example: v1.4.0 on Linux amd64). `/usr/local/bin` also suits the
system service, which needs a root-owned path:

```sh
tar -xzf node-tracker_1.4.0_linux_amd64.tar.gz && sudo install -m 755 node-tracker_1.4.0_linux_amd64/node-tracker /usr/local/bin/
```

The archive holds the binary, its man page (`node-tracker.1`), the README,
the docs, the systemd units and an example config.

## Container image

`ghcr.io/al-maisan/node-tracker`, linux/amd64, arm64, arm/v6; from v1.1.1
on. Built from the release archives. Run it with the Compose files in
[deploy/compose](../deploy/compose); see [Operations](operations.md#container).

## Go

Go 1.26 or newer:

```sh
go install github.com/al-maisan/node-tracker/cmd/node-tracker@latest
```

This puts `node-tracker` in `~/go/bin`, which must be on your `PATH`. From
a clone, `make build` puts it in `bin/node-tracker`.

## Verify a download

In the directory with the download, `SHA256SUMS` and `SHA256SUMS.asc`:

1. Import the release key:

   ```sh
   curl -s https://github.com/al-maisan.gpg | gpg --import
   ```

   You should see `key 1E0A288397AE739E: public key "Muharem Hrnjadovic
   …" imported` (or `not changed`). Lines such as `420 signatures not
   checked due to missing keys` and `no ultimately trusted keys found` are
   normal.

2. Check the signature of the checksums:

   ```sh
   gpg --verify SHA256SUMS.asc SHA256SUMS
   ```

   You should see `Good signature from "Muharem Hrnjadovic …"` and
   `using RSA key 753B6ECF2B458FF3D19D568C1E0A288397AE739E`. The warning
   that the key is not certified is expected; any other key, or `BAD
   signature`, means stop.

3. Check the download against them:

   ```sh
   sha256sum --ignore-missing -c SHA256SUMS
   ```

   You should see your file's name followed by `OK`, e.g.
   `node-tracker_1.4.0-1_arm64.deb: OK`. On macOS use
   `shasum -a 256 --ignore-missing -c SHA256SUMS`.

For a script, this checks the same and fails unless the release key
signed:

```sh
gpg --status-fd 1 --verify SHA256SUMS.asc SHA256SUMS 2>/dev/null | grep -q '^\[GNUPG:\] VALIDSIG .* 753B6ECF2B458FF3D19D568C1E0A288397AE739E$' && sha256sum --ignore-missing -c SHA256SUMS
```
