package player

import (
	"testing"
	"time"

	"mygo-music/internal/music"
)

// What the listener does reaches Hear as it does it: pause, play, seek and
// ended, with where the track is and how much of it was heard.
func TestHear(t *testing.T) {
	ch, p := posted()
	defer p.out.Close()
	var events []music.PlayerEvent
	p.Hear = func(ev music.PlayerEvent) { events = append(events, ev) }

	p.TogglePlay() // paused
	if len(events) != 1 || events[0].Type != "pause" {
		t.Fatalf("pausing heard %+v", events)
	}
	if events[0].TrackID != "1" {
		t.Errorf("the pause says nothing of the track: %+v", events[0])
	}
	p.TogglePlay() // playing again, so the play is counted
	if len(events) != 2 || events[1].Type != "play" {
		t.Fatalf("playing heard %+v", events)
	}
	p.Seek(90 * time.Second)
	next(t, ch) // the seek lands on the main thread
	if n := len(events); n < 3 || events[n-1].Type != "seek" {
		t.Fatalf("seeking heard %+v", events)
	}
	p.ended()
	if n := len(events); n != 4 || events[3].Type != "ended" {
		t.Fatalf("ending heard %+v", events)
	}
	end := events[3]
	if end.Reason != music.EndFinished || end.TrackID != "1" {
		t.Errorf("the end: %+v", end)
	}
	if end.Seconds < 0 || end.Seconds > 5 {
		t.Errorf("heard %vs of a play that sought to 90s and ended at once", end.Seconds)
	}
}

// What's heard is what played: what a seek jumps over isn't, what a seek back
// played again is, and a play the app quit in the middle of is said so. A new
// play counts from nothing.
func TestHeardAccountsWhatPlayed(t *testing.T) {
	p := &Player{at: -1}
	p.Cue([]music.Track{{ID: "1", Duration: 600000}, {ID: "2"}})
	p.playing = true
	p.listen()
	p.jumped(100 * time.Second) // it had played to 100s
	p.heardTo(105 * time.Second)
	p.jumped(20 * time.Second) // back to 20s, which plays again
	p.heardTo(50 * time.Second)
	p.pending = 60 * time.Second // it stands there as the app quits
	ev := p.Quit()
	if ev == nil {
		t.Fatal("quitting heard nothing")
	}
	if ev.Type != "ended" || ev.Reason != music.EndQuit || ev.TrackID != "1" {
		t.Errorf("quitting heard %+v", ev)
	}
	if ev.Seconds != 45 || ev.Position != 60000 {
		t.Errorf("heard %vs at %vms, want 45s at 60000ms", ev.Seconds, ev.Position)
	}
	if again := p.Quit(); again != nil {
		t.Errorf("quit again: %+v", again)
	}

	// A play taken up again counts from nothing, and hears of its end.
	var got []music.PlayerEvent
	p.Hear = func(ev music.PlayerEvent) { got = append(got, ev) }
	p.listen()
	p.heardTo(70 * time.Second)
	p.endPlay(music.EndSkipped, 70*time.Second)
	if len(got) != 1 || got[0].Reason != music.EndSkipped || got[0].Seconds != 10 {
		t.Errorf("the next play: %+v", got)
	}
}
