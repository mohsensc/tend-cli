package deliver

import (
	"testing"
	"time"
)

func TestDeclined(t *testing.T) {
	tests := []struct {
		name  string
		close func(f *FakeForge)
		want  bool
	}{
		{"open", func(*FakeForge) {}, false},
		{"merged", func(f *FakeForge) { noErr(t, f.Merge(1)) }, false},
		{"closed by tend", func(f *FakeForge) { noErr(t, Close(bg, f, 1, "Closing to make room for a security fix.")) }, false},
		{"closed by tend and replaced", func(f *FakeForge) {
			noErr(t, Close(bg, f, 1, "Closing to make room for a security fix."))
			noErr(t, Replaced(bg, f, 1, 2))
		}, false},
		{"closed by a maintainer", func(f *FakeForge) { noErr(t, f.CloseByHuman(1)) }, true},
		{"closed by a maintainer with a word", func(f *FakeForge) {
			noErr(t, f.Say(1, "maintainer", "OWNER", "not now"))
			noErr(t, f.CloseByHuman(1))
		}, true},
		{"closed by tend, reopened and closed by a maintainer", func(f *FakeForge) {
			noErr(t, Close(bg, f, 1, "Closing: no longer needed."))
			noErr(t, f.Reopen(1))
			noErr(t, f.Say(1, "maintainer", "OWNER", "we still do not want it"))
			noErr(t, f.CloseByHuman(1))
		}, true},
		{"closed by tend, then a bot comments", func(f *FakeForge) {
			noErr(t, Close(bg, f, 1, "Closing: no longer needed."))
			noErr(t, f.Say(1, "codecov[bot]", "NONE", "Coverage unchanged."))
		}, false},
		{"closed by tend, then a bot with write access says skip", func(f *FakeForge) {
			noErr(t, Close(bg, f, 1, "Closing: no longer needed."))
			noErr(t, f.Say(1, "helper[bot]", "MEMBER", "tend skip"))
		}, false},
		{"closed by a bot", func(f *FakeForge) {
			noErr(t, f.Say(1, "stale[bot]", "NONE", "Closing as stale."))
			noErr(t, f.CloseAs(1, "stale[bot]"))
		}, false},
		{"closed by a bot after a maintainer said skip", func(f *FakeForge) {
			noErr(t, f.Say(1, "maintainer", "MEMBER", "tend skip"))
			noErr(t, f.CloseAs(1, "stale[bot]"))
		}, true},
		{"closed by tend after a maintainer said skip", func(f *FakeForge) {
			noErr(t, f.Say(1, "maintainer", "MEMBER", "tend skip"))
			noErr(t, f.Say(1, tendLogin, "NONE", "Skipped as @maintainer asked."))
			noErr(t, f.ClosePR(bg, 1))
		}, true},
		{"closed by tend without a word", func(f *FakeForge) { noErr(t, f.ClosePR(bg, 1)) }, false},
		{"a contributor's skip is not a no", func(f *FakeForge) {
			noErr(t, f.Say(1, "drive-by", "CONTRIBUTOR", "tend skip"))
			noErr(t, f.CloseAs(1, "stale[bot]"))
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFake("acme", "shop", "main")
			tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
			tt.close(f)
			got, err := Declined(bg, f, f.PR(1))
			if err != nil || got != tt.want {
				t.Errorf("Declined = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// tend running on a person's token closes as that person; a later comment,
// from anyone, does not make the close a maintainer's no. A maintainer who
// reopens and closes again, minutes on and without a word, does say no.
func TestDeclinedByTime(t *testing.T) {
	tests := []struct {
		name  string
		after func(f *FakeForge, tick func(time.Duration))
		want  bool
	}{
		{"tend's close, then a contributor's thanks", func(f *FakeForge, tick func(time.Duration)) {
			tick(time.Hour)
			noErr(t, f.Say(1, "contrib", "CONTRIBUTOR", "thanks"))
		}, false},
		{"tend's close, then the token's own person comments", func(f *FakeForge, tick func(time.Duration)) {
			tick(time.Hour)
			noErr(t, f.Say(1, "alice", "OWNER", "ok"))
		}, false},
		{"reopened and closed again without a word", func(f *FakeForge, tick func(time.Duration)) {
			tick(time.Hour)
			noErr(t, f.Reopen(1))
			tick(time.Hour)
			noErr(t, f.CloseAs(1, "maintainer"))
		}, true},
		{"reopened and closed again by the token's own person", func(f *FakeForge, tick func(time.Duration)) {
			tick(time.Hour)
			noErr(t, f.Reopen(1))
			tick(time.Hour)
			noErr(t, f.CloseAs(1, "alice"))
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			f := NewFake("acme", "shop", "main")
			f.Login, f.Now = "alice", func() time.Time { return now }
			tendPR(t, f, "tend/npm-a-2.0.0", "npm/a@2.0.0")
			noErr(t, Close(bg, f, 1, "Closing to make room for a security fix."))
			tt.after(f, func(d time.Duration) { now = now.Add(d) })
			got, err := Declined(bg, f, f.PR(1))
			if err != nil || got != tt.want {
				t.Errorf("Declined = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}
