# Woosh

A CLI tool to copy text snippets and files between machines on your local network.

One side offers, the other side takes it. No daemon, no account, no cloud. The bytes go
straight from one machine to the other over TLS.

```
# machine A
$ woosh send ./report.pdf
code: 7-canyon-otter
sending report.pdf (2.4 MB), waiting 5m0s for a receiver...

# machine B
$ woosh recv 7-canyon-otter
found an offer from studio at 192.168.1.24
receiving report.pdf (2.4 MB) from studio
got 2.4 MB into ./report.pdf
```

## Install

```
go install github.com/yavosh/woosh@latest
```

Go 1.27.2 or later. Woosh has no dependencies outside the standard library.

## Set up

Every machine in your woosh network shares one key. Pick whichever of these three suits you.
They are three ways to reach the same 32-byte key, not three kinds of key.

**Eight words, to read out or type over.** This is what plain `woosh init` gives. Easier to
get right than 52 base32 characters:

```
$ woosh init
network key saved to <config dir>/woosh/key

  oak depend diary forum
  ill become become lawsuit

type these on your other machines:
  woosh init --key "oak depend diary forum ill become become lawsuit"

keep them secret: they are the only thing that guards a transfer.
```

**A passphrase, with nothing to copy.** Run the same command on every machine and type the
same phrase:

```
$ woosh init --passphrase
type the same passphrase on every machine in your woosh network.
it needs 12 characters or more.

passphrase:
passphrase again:
deriving the key, this takes a moment...
network key saved to <config dir>/woosh/key
```

The phrase never leaves the machine and is never stored. What is typed does not appear on
screen. Woosh asks twice, because a typo here gives you a key nobody else can reach.

**The base32 key**, which is what a password manager or a config-management tool wants:

```
$ woosh init --raw
  WSH-PDAL-FKTV-CSZN-...
$ woosh init --key WSH-PDAL-FKTV-CSZN-...   # on the others
```

`--key` takes either form, so you can paste words or base32 and it works out which it is.

The key is the only thing that guards a transfer. Keep it secret.

The key file goes under the directory your operating system keeps program configuration in.
Woosh does not pick a path of its own, and `woosh init` prints the one it used.

`WOOSH_KEY` overrides the key file when it is set, which is useful for a throwaway network
or for a script. `woosh init --passphrase` also reads a piped phrase, so setup can be scripted:

```
$ echo "$PHRASE" | woosh init --passphrase
```

### Which one to pick

|  | Nothing to copy | Strength | Good for |
|---|---|---|---|
| words (the default) | no, 8 words | 88 bits | reading down a phone, typing on a laptop |
| `--passphrase` | yes | as strong as the phrase | machines you set up by hand |
| `--raw` | no, 52 characters | 256 bits | password managers, scripts, config management |

A passphrase is stretched with PBKDF2-HMAC-SHA256 at 4,000,000 rounds. The salt has to be a
constant, or two machines could never agree from the phrase alone, so that round count is the
only thing standing between a weak phrase and a precomputed table. Pick a real passphrase, not
a password. The 12-character floor is a guard against slips, not a measure of strength.

The words come from the BIP-39 English list, whose entries are unique in their first four
letters, so a misspelling is caught rather than silently accepted as a different key.

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
| `--passphrase` | init | derive the key from a phrase typed on every machine |
| `--raw` | init | make a key and show it as base32 instead of words |
| `--key` | init | adopt a key, as words or as base32 |
| `--force` | init | replace a key that is already there |
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
3. Both sides make a throwaway certificate and run TLS 1.3.
4. Both sides prove they hold the network key, with an HMAC over the two certificate hashes
   and the offer id. A machine in the middle terminates TLS with its own certificates, so its
   proof does not verify and the transfer stops.
5. The sender describes the payload, the receiver accepts, and the bytes flow as
   length-prefixed chunks with a SHA-256 at the end.
6. Files stay in unique `.woosh-partial-*` staging paths until the checksum matches.
   Failed transfers remove their staging paths. Archive publication preflights conflicts
   and rolls back failures. If publication fails, the error names the retained staging
   directory for recovery. Several archive entries cannot appear atomically as one update.
   Stdout streams bytes before checksum verification.
7. The receiver confirms delivery after verification and publication. The sender reports
   success only after this receipt. Both peers must support wire version 2; older builds
   fail with a version mismatch. A lost receipt leaves delivery unconfirmed even if the
   receiver committed the files.

Publication without overwriting requires hard-link support on the destination filesystem.
Unsupported filesystems fail without replacing existing files.

## What it does not do

- Talk over the internet, or across NAT. Local network only.
- Touch your clipboard. Pipe `pbpaste` and `pbcopy` yourself.
- Carry symlinks, sockets, or device files. Woosh sends regular files and directories, and
  reports what it skipped.
- Accept sparse archive encodings. Receivers reject them to prevent disk expansion beyond
  the transferred payload. `--max-size` limits transferred bytes.
- Resume an interrupted transfer.
- Tell your machines apart. One shared key means every machine that holds it is equally
  trusted.

## Code

Simple idiomatic Go, more code less libraries. One flat `package main`, standard library only.
Key derivation uses `crypto/pbkdf2` and `crypto/hkdf`, both of which the standard library has
carried since Go 1.24.

`bip39.txt` is the BIP-39 English word list, embedded with `go:embed`. It is data, not code.

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
