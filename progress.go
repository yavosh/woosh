package main

import (
	"fmt"
	"io"
	"time"
	"unicode"
)

// progress counts bytes on their way through, and draws a one-line report on
// out. A total of zero or less means the size is not known in advance.
type progress struct {
	out    io.Writer
	total  int64
	n      int64
	last   time.Time
	drawn  bool
	silent bool
}

func newProgress(out io.Writer, total int64, silent bool) *progress {
	return &progress{out: out, total: total, silent: silent}
}

func (p *progress) Write(b []byte) (int, error) {
	p.n += int64(len(b))
	if !p.silent && time.Since(p.last) > 100*time.Millisecond {
		p.last = time.Now()
		p.draw("\r")
	}
	return len(b), nil
}

func (p *progress) done() {
	if p.silent {
		return
	}
	if p.drawn {
		p.draw("\r")
	}
	fmt.Fprintln(p.out)
}

func (p *progress) draw(prefix string) {
	p.drawn = true
	if p.total > 0 {
		fmt.Fprintf(p.out, "%s  %s of %s (%d%%)   ", prefix,
			humanBytes(p.n), humanBytes(p.total), 100*p.n/p.total)
		return
	}
	fmt.Fprintf(p.out, "%s  %s   ", prefix, humanBytes(p.n))
}

// cleanText makes a string that came off the network safe to show. An announce
// is not authenticated, so anyone on the network can put escape sequences in
// the name it carries, and a terminal acts on those. It also caps the length,
// because a long name would break up the ls table.
func cleanText(s string) string {
	const maxRunes = 40
	out := make([]rune, 0, maxRunes)
	for _, r := range s {
		if !unicode.IsPrint(r) {
			r = '?'
		}
		out = append(out, r)
		if len(out) == maxRunes {
			return string(out) + "..."
		}
	}
	return string(out)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}
