package player

import (
	"testing"
	"time"

	"mygo-music/internal/music"
)

// What the listener does reaches Hear as it does it: pause, play, seek and
// ended, with where the track is.
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
	p.TogglePlay() // playing again
	if len(events) != 2 || events[1].Type != "play" {
		t.Fatalf("playing heard %+v", events)
	}
	p.Seek(90 * time.Second)
	next(t, ch) // the seek's landing posts to the main thread
	if n := len(events); n < 3 || events[n-1].Type != "seek" {
		t.Fatalf("seeking heard %+v", events)
	}
	p.ended()
	if n := len(events); n < 4 || events[n-1].Type != "ended" {
		t.Fatalf("ending heard %+v", events)
	}
}
