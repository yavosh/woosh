# Woosh

A CLI tool to copy text snippets and files between machines on your local network.

One side offers, the other side takes it. No daemon, no account, no cloud. The bytes go
straight from one machine to the other over TLS.

```
# machine A
$ woosh send ./report.pdf
device: studio
fingerprint: SHA256:...
code: 7-canyon-otter
sending report.pdf (2.4 MB), waiting 5m0s for a receiver...

# machine B
$ woosh recv 7-canyon-otter
device: laptop
fingerprint: SHA256:...
found an offer from studio at 192.168.1.24
receiving report.pdf (2.4 MB) from studio
got 2.4 MB into ./report.pdf
```

First contact asks both machines to confirm and remember the peer's public key.

## Install

```
go install github.com/yavosh/woosh@latest
```

Go 1.27.2 or later. Woosh has no dependencies outside the standard library.

## Set up

Each device has its own persistent Ed25519 identity. Shared network keys are no longer required.
`send` and `recv` create an identity automatically when needed.

Run `woosh init` to create an identity or display its public fingerprint:

```sh
woosh init
```

The device name defaults to the hostname. Choose a unique name before your first transfer if hostnames overlap:

```sh
woosh init --name studio
```

On first contact, **both machines** show the other device's name and public fingerprint:

```text
Unknown device "studio" at 192.168.1.24:54321.
ED25519 fingerprint: SHA256:...
Compare this fingerprint with "woosh init" on that device.
Trust this device and save its public key? (yes/no):
```

Compare each fingerprint with the fingerprint printed on the other machine.
Type `yes` on both machines to save trust and start the transfer.
Subsequent transfers verify the saved public keys without asking again, including transfers in the opposite direction.

Confirmation reads from the controlling terminal. It does not consume snippet stdin or write into payload stdout.
Without a terminal, an unknown device is rejected. Previously trusted devices work in scripts.
First-use confirmation times out after five minutes. Typing anything except `yes` declines trust.
When both devices report saved trust, automatic approval times out after five seconds.
This releases stalled connections so another receiver can retry the offer.
Waiting for peer approval shows a status message after one second.

The identity file and `known_peers` directory live under your operating system's configuration directory, inside `woosh`.
On Unix, these files use mode `0600`, and new configuration directories use mode `0700`.
`woosh init` prints the identity path.
Never copy the identity file between machines. It contains the device's private key.

Trust binds a device name to its public key. If the key changes, Woosh blocks the connection without replacing saved trust.
A device with a new name requires a new confirmation.
For an intentional key replacement, verify the new fingerprint before removing the known-peer file named in the error.

Upgrade both machines. Older shared-key versions cannot connect to this protocol.
Existing network-key files are left untouched. `WOOSH_KEY`, `--key`, `--raw`, and `--passphrase` are no longer used.

## Use

**Send a file**

```
$ woosh send ./report.pdf
```

**Send a snippet**

```
$ echo "ssh-ed25519 AAAA..." | woosh send
$ pbpaste | woosh send --name notes.md
```

**Send a directory, or several paths**

```
$ woosh send ./project ./todo.txt
```

These go as one tar stream. The receiver gets `project/` and `todo.txt` side by side.

**Take an offer**

```
$ woosh recv 7-canyon-otter        # writes ./report.pdf
$ woosh recv 7-canyon-otter -o ~/Downloads
$ woosh recv                       # the only offer on the network
$ woosh recv 3-amber-pine | jq .   # a snippet on a pipe goes to stdout
```

A single file goes to stdout when stdout is not a terminal, and to a file when it is.
`--stdout` and `-o` override that.

**See what is waiting**

```
$ woosh ls
ID            FROM     ADDRESS
137cfa83ae58  studio   192.168.1.24:56462
```

An announce carries no file name and no size, so `ls` cannot show what an offer holds. You
need the code for that.

### Flags worth knowing

| Flag | Command | What it does |
|---|---|---|
| `--name` | init | choose a unique device name when creating an identity |
| `--code` | send | use a code you choose |
| `--name` | send | the file name to give a snippet from stdin |
| `--timeout` | send | give up if nobody comes, 5m by default |
| `--keep` | send | serve the offer more than once |
| `-o` | recv | a directory, or a file name for a single file |
| `--force` | recv | replace a file that is already there |
| `--max-size` | recv | refuse a payload above this many bytes |
| `--wait` | recv | how long to look for the offer, 3s by default |

`recv` takes its code on either side of the flags. `send` needs its flags before the paths,
because anything after the first path is a path: `woosh send --keep ./a ./b`.

Run `recv --wait 30s` when you want to start the receiver before the sender.

## How it works

1. `send` listens on a random TCP port and announces `{version, offer id, port, hostname}`
   to the multicast group `239.255.42.99:9977`, once a second. The offer id is a hash of the
   code, so the code itself never goes on the network.
2. `recv` listens on the same group and matches the offer id. It takes the sender's address
   from the datagram itself, never from a field inside it.
3. Both sides run TLS 1.3 with certificates backed by their persistent Ed25519 identities.
   TLS proves possession of each private key.
4. The peers exchange authentication versions and offer IDs inside TLS.
   Each side checks saved trust or requests first-use confirmation.
   Both sides must approve before payload metadata or bytes are sent.
5. The sender describes the payload, the receiver accepts, and the bytes flow as
   length-prefixed chunks with a SHA-256 at the end.
6. Files stay in unique `.woosh-partial-*` staging paths until the checksum matches.
   Failed transfers remove their staging paths. Archive publication preflights conflicts
   and rolls back failures. If publication fails, the error names the retained staging
   directory for recovery. Several archive entries cannot appear atomically as one update.
   Stdout streams bytes before checksum verification.
7. The receiver confirms delivery after verification and publication. The sender reports
   success only after this receipt. Both peers must support device trust and wire version 2.
   A lost receipt leaves delivery unconfirmed even if the receiver committed the files.

Archive publication and file publication without overwriting require hard-link support on the destination filesystem.
Publication errors identify retained recovery paths when rollback cannot complete.
Cleanup failures after successful archive publication produce a warning and retain recovery paths.
They do not change the delivery receipt to a failure.
Cancellation closes active connections and closable streams. The CLI allows one second
for cleanup, then exits even if an inherited stdin or stdout syscall remains blocked.
Forced shutdown can leave partially published archive entries and retained staging or backups.
Inspect these paths for recovery before removing them.

## What it does not do

- Talk over the internet, or across NAT. Local network only.
- Touch your clipboard. Pipe `pbpaste` and `pbcopy` yourself.
- Carry symlinks, sockets, or device files. Woosh sends regular files and directories, and
  reports what it skipped.
- Accept sparse archive encodings. Receivers reject them to prevent disk expansion beyond
  the transferred payload. `--max-size` limits transferred bytes.
- Resume an interrupted transfer.

## Code

Simple idiomatic Go, more code less libraries. One flat `package main`, standard library only.
Device identities use `crypto/ed25519`. TLS certificates renew while the identity public key stays stable.

```
make            # build
make test       # every test, including the ones that need multicast
make test-short # skip the multicast tests
make quality    # the guards below
make check      # quality + full tests + build
make help       # list the targets
```

CI runs the same Makefile targets on ubuntu and macos, so a green build locally means a green
build there.

| Guard | What it protects |
|---|---|
| `fmt-check` | formatting stays gofmt clean |
| `vet` | the standard suspicious-construct checks |
| `staticcheck` | a deeper linter, pinned to one version |
| `vulncheck` | known holes in the standard library and toolchain |
| `no-deps` | fails the day anything enters `go.mod`, because woosh has no dependencies |
| `cross` | linux, macOS and windows on amd64 and arm64 still compile |
| `cover` | statement coverage stays above 60% |

The linters run through `go run tool@version`, so they never enter `go.mod` themselves. Both
are pinned: an unpinned tool turns a green build red on somebody else's release day.
