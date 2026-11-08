package player

import (
	"time"

	"mygo-music/internal/music"
)

// A play of a track lasts from when it starts to play until something else
// plays, it plays to its end, or the app quits. The player keeps how much of
// it is heard: how far its position moved on, from where the play began and
// from each seek, so what's paused, waited for its audio or jumped over
// doesn't count, and what a seek back plays again does. It's added up only as
// the position jumps and as the play ends, never as it plays.
//
// The player doesn't know who hears of it: Hear takes the ended event to the
// app, which tells the plugins that export onEvent.

// quitWait is how long quitting waits for a source to hear how its last play
// went.
const quitWait = 2 * time.Second

// listen starts a play under way of the current track: it's counting from
// nothing, unless one is already under way.
func (p *Player) listen() {
	if p.listening || p.track == nil || !p.playing {
		return
	}
	p.listening, p.heard, p.mark = true, 0, p.Position()
}

// jumped notes that the position jumps to t, the play going on from there.
func (p *Player) jumped(t time.Duration) {
	if p.listening {
		p.heardTo(p.Position())
		p.mark = max(t, 0)
	}
}

// heardTo counts what was heard from the mark to pos, where the next begins.
func (p *Player) heardTo(pos time.Duration) {
	if pos > p.mark {
		p.heard += pos - p.mark
	}
	p.mark = pos
}

// endPlay ends the play under way at pos, and hears of it: how much of it was
// heard, and why it ended.
func (p *Player) endPlay(why music.EndReason, pos time.Duration) {
	if !p.listening {
		return
	}
	p.listening = false
	p.heardTo(pos)
	p.heardEvent(why, pos)
}

// heardEvent hears of the play that ended, with how much of it was heard.
func (p *Player) heardEvent(why music.EndReason, pos time.Duration) {
	if p.Hear == nil {
		return
	}
	ev := music.PlayerEvent{
		Type:     "ended",
		Position: pos.Milliseconds(),
		Duration: p.Duration().Milliseconds(),
		Seconds:  p.heard.Seconds(),
		Reason:   why,
	}
	if p.track != nil {
		ev.TrackID = p.track.ID
	}
	p.Hear(ev)
}

// Quit ends the play under way as the app quits, and answers what the plugins
// are to hear of it, for the app to tell them itself, and wait for. Unlike
// the other ways a play ends, it hears nothing as it goes: what the app tells
// the plugins as it quits, it tells them once, itself.
func (p *Player) Quit() *music.PlayerEvent {
	if !p.listening {
		return nil
	}
	p.listening = false
	p.heardTo(p.Position())
	return p.event(music.EndQuit, p.Position())
}

// event builds the ended event of the play that just ended.
func (p *Player) event(why music.EndReason, pos time.Duration) *music.PlayerEvent {
	ev := music.PlayerEvent{
		Type:     "ended",
		Position: pos.Milliseconds(),
		Duration: p.Duration().Milliseconds(),
		Seconds:  p.heard.Seconds(),
		Reason:   why,
	}
	if p.track != nil {
		ev.TrackID = p.track.ID
	}
	return &ev
}
