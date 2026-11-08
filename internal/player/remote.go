package player

import (
	"errors"
	"sync"
	"time"

	"mygo-music/internal/i18n"
	"mygo-music/internal/music"
)

// Remote playback: a track of a PlaybackRemote source plays elsewhere, in the
// source's own app, and the player only tells it what to do, and asks, about
// once a second while it plays, how it's going. Between answers its position
// runs on by the clock. Paused, it asks nothing, and the app sleeps.

const (
	// remotePollEvery is how often a playing source is asked how it's going.
	remotePollEvery = time.Second
	// remoteGrace is how many answers a source may still speak of what it
	// played before, having been told to play something else.
	remoteGrace = 4
	// remoteDrift is how far a source's position may be from the clock's
	// without anything showing it.
	remoteDrift = 1500 * time.Millisecond
	// remoteSettle is how long after a command the answers may be from before
	// it landed, and don't count.
	remoteSettle = 1500 * time.Millisecond
	// remoteEndZone is how near its end a track is said to have ended, when
	// the source stops there.
	remoteEndZone = 2500 * time.Millisecond
	// remoteFailures is how many answers in a row may fail before the player
	// gives up on a source.
	remoteFailures = 3
)

// remote is how a track of a remote source is going.
type remote struct {
	src     music.Source
	started bool // the source was told to play it
	seen    bool // the source was seen playing it
	polling bool // an answer is on its way
	polls   int  // answers since it was told, until it was seen playing it
	fails   int  // answers failed in a row

	pos      time.Duration // the position at the time of at
	at       time.Time
	running  bool // the clock runs
	near     bool // it was last near its end
	duration time.Duration
	command  time.Time // the last command that moved it
}

// commands runs a source's commands in the order they were given, off the main
// thread, for a pause and a play not to pass each other on the way to a plugin.
type commands struct {
	mu   sync.Mutex
	list []func()
	busy bool
}

func (c *commands) add(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, fn)
	if !c.busy {
		c.busy = true
		go c.run()
	}
}

func (c *commands) run() {
	for {
		c.mu.Lock()
		if len(c.list) == 0 {
			c.busy = false
			c.mu.Unlock()
			return
		}
		fn := c.list[0]
		c.list = c.list[1:]
		c.mu.Unlock()
		fn()
	}
}

// command gives a source a command; if it fails, the bar says so.
func (p *Player) command(src music.Source, fn func() error) {
	gen := p.gen
	p.cmds.add(func() {
		if err := fn(); err != nil {
			p.post(func() {
				if gen == p.gen {
					p.remoteFailed(src, err)
				}
			})
		}
	})
}

// remoteFailed says why a remote source didn't do what it was told.
func (p *Player) remoteFailed(src music.Source, err error) {
	p.err = errorText(err)
	if errors.Is(err, music.ErrSignIn) || errors.Is(err, music.ErrSetup) {
		p.playing = false
		if p.remote != nil {
			p.remote.running = false
		}
		if p.Failed != nil {
			p.Failed(src.Info().ID, err)
		}
	}
	p.sync()
}

// loadRemote tells src to play the track at order[at], if play, or else just
// keeps it, to tell once asked: a session picked up shouldn't start anything.
func (p *Player) loadRemote(gen int, src music.Source, at, skipped, dir int, play bool, from time.Duration) {
	r := &remote{src: src}
	p.remote = r
	if !play {
		p.loading = false
		p.sync()
		return
	}
	ids, start := p.remoteQueue(src.Info().ID, at)
	p.cmds.add(func() {
		err := src.RemotePlay(ids, start, from.Milliseconds())
		p.post(func() {
			if gen != p.gen {
				return
			}
			p.loading = false
			if err != nil {
				p.remote = nil
				p.loadFailed(src.Info().ID, err, at, skipped, dir)
				return
			}
			r.started, r.running = true, true
			r.pos, r.at, r.command = from, p.now(), p.now()
			p.pending = 0
			if p.playing {
				p.record()
				p.listen()
				p.remotePoll(r)
			} else {
				// Paused while it was on its way.
				p.playing = true
				p.remoteToggle()
			}
			p.sync()
		})
	})
}

// remoteQueue returns the IDs of the queue's tracks of source, in play order,
// and where among them order[at] is: the source goes on through them itself,
// as it would with the app closed.
func (p *Player) remoteQueue(source string, at int) ([]string, int) {
	var ids []string
	start := 0
	for k, i := range p.order {
		if k == at {
			start = len(ids)
		}
		if p.queue[i].Source == source {
			ids = append(ids, p.queue[i].ID)
		}
	}
	return ids, start
}

// remotePosition is where the track is by the last answer and the clock.
func (p *Player) remotePosition() time.Duration {
	r := p.remote
	pos := r.pos
	if r.running {
		pos += p.now().Sub(r.at)
	}
	if r.duration > 0 {
		pos = min(pos, r.duration)
	} else if d := p.track.Length(); d > 0 {
		pos = min(pos, d)
	}
	return pos
}

// remoteToggle pauses or plays on.
func (p *Player) remoteToggle() {
	r := p.remote
	if p.playing {
		r.pos, r.running = p.remotePosition(), false
		r.at, r.command = p.now(), p.now()
		p.playing = false
		p.command(r.src, r.src.RemotePause)
		p.sync()
		return
	}
	p.playing = true
	r.running, r.at, r.command = true, p.now(), p.now()
	p.listen()
	p.command(r.src, r.src.RemoteResume)
	p.remotePoll(r)
	p.sync()
}

// remoteSeek moves the track, showing it there at once.
func (p *Player) remoteSeek(t time.Duration) {
	r := p.remote
	r.pos, r.at, r.command = t, p.now(), p.now()
	r.near = false
	src := r.src
	ms := t.Milliseconds()
	p.command(src, func() error { return src.RemoteSeek(ms) })
	p.sync()
}

// remoteVolume sets the source's volume, only the last of those asked while
// the one before is on its way: a slider asks for many.
func (p *Player) remoteVolume(v float64) {
	r := p.remote
	if !r.started {
		return
	}
	p.cmds.mu.Lock()
	queued := p.volumeQueued
	p.volumeAsked, p.volumeQueued = int(v*100+0.5), true
	p.cmds.mu.Unlock()
	if queued {
		return
	}
	src := r.src
	p.command(src, func() error {
		p.cmds.mu.Lock()
		percent := p.volumeAsked
		p.volumeQueued = false
		p.cmds.mu.Unlock()
		return src.RemoteVolume(percent)
	})
}

// remotePoll asks the source how it's going in a moment, unless it's asked
// already. Nothing is asked unless the listener wants it to play.
func (p *Player) remotePoll(r *remote) {
	if r.polling || !p.playing || p.remote != r {
		return
	}
	r.polling = true
	p.after(remotePollEvery, func() {
		st, err := r.src.RemoteState()
		quiet := p.Quiet
		if quiet == nil {
			quiet = p.post
		}
		quiet(func() {
			r.polling = false
			if p.remote == r && p.playing && r.started {
				p.remoteAnswered(r, st, err)
			}
		})
	})
}

// remoteAnswered takes how the source says it's going, and redraws only if
// that changed what shows.
func (p *Player) remoteAnswered(r *remote, st *music.RemoteState, err error) {
	if err != nil {
		r.fails++
		if errors.Is(err, music.ErrSignIn) || errors.Is(err, music.ErrSetup) || r.fails >= remoteFailures {
			p.playing, r.running = false, false
			r.pos, r.at = p.remotePositionStopped(r), p.now()
			p.remoteFailed(r.src, err)
			p.post(func() {})
			return
		}
		p.remotePoll(r)
		return
	}
	r.fails = 0
	if !r.seen {
		r.polls++
	}
	waiting := !r.seen && r.polls < remoteGrace
	settling := p.now().Sub(r.command) < remoteSettle
	switch {
	case st == nil:
		// Nothing plays there.
		if waiting {
			p.remotePoll(r)
			return
		}
		why := music.EndStopped
		if r.near {
			why = music.EndFinished
		}
		p.remoteEnded(r, why)
	case st.TrackID != "" && st.TrackID != p.track.ID:
		if waiting {
			p.remotePoll(r)
			return
		}
		p.remoteMoved(r, st)
	case !st.Playing:
		if waiting || settling {
			p.remotePoll(r)
			return
		}
		pos := time.Duration(st.Position) * time.Millisecond
		end := r.duration
		if end <= 0 {
			end = p.track.Length()
		}
		if r.near || end > 0 && pos >= end-remoteEndZone {
			p.remoteEnded(r, music.EndFinished)
			return
		}
		// Paused there, by the listener at the other end.
		r.pos, r.at, r.running = pos, p.now(), false
		p.playing = false
		p.sync()
		p.post(func() {})
	default:
		p.remoteGoes(r, st, settling)
		p.remotePoll(r)
	}
}

// remotePositionStopped is where the track is as the clock stops.
func (p *Player) remotePositionStopped(r *remote) time.Duration {
	r.running = true
	defer func() { r.running = false }()
	return p.remotePosition()
}

// remoteGoes takes the position of the track playing, as it's meant to.
func (p *Player) remoteGoes(r *remote, st *music.RemoteState, settling bool) {
	r.seen = true
	if settling {
		return
	}
	before := p.remotePosition()
	pos := time.Duration(st.Position) * time.Millisecond
	switch d := pos - before; {
	case d > remoteDrift, d < -remoteDrift && pos < r.pos:
		// Moved at the other end: what it jumped over wasn't heard.
		p.jumped(pos)
	case d < -remoteDrift && p.listening:
		// Slower than the clock: heard only as far as it got.
		p.heardTo(pos)
	}
	duration := time.Duration(st.Duration) * time.Millisecond
	shown := duration > 0 && duration != r.duration
	if duration > 0 {
		r.duration = duration
	}
	r.pos, r.at, r.running = pos, p.now(), true
	end := r.duration
	if end <= 0 {
		end = p.track.Length()
	}
	r.near = end > 0 && pos >= end-remoteEndZone
	if d := pos - before; d > remoteDrift || d < -remoteDrift {
		shown = true
	}
	if shown {
		p.sync()
		p.post(func() {})
	}
}

// remoteEnded goes on from the track the source finished, or stopped, by the
// queue's order and the listener's repeat.
func (p *Player) remoteEnded(r *remote, why music.EndReason) {
	p.endPlay(why, p.remotePosition())
	switch {
	case p.repeat == RepeatOne:
		p.load(p.at)
	case p.at+1 < len(p.order):
		p.load(p.at + 1)
	case p.feed != nil:
		p.stopForFeed()
		p.awaitFeed()
	case p.repeat == RepeatAll:
		p.load(0)
	default:
		p.playing, r.running = false, false
		r.pos, r.at, r.near = 0, p.now(), false
		p.sync()
	}
	p.post(func() {})
}

// remoteMoved follows the source to the track it went on to, by itself or told
// to at the other end. Repeating one, it goes back. One the queue doesn't have
// isn't the app's to show: it stops following.
func (p *Player) remoteMoved(r *remote, st *music.RemoteState) {
	defer p.post(func() {})
	why := music.EndSkipped
	if r.near {
		why = music.EndFinished
	}
	if p.repeat == RepeatOne {
		p.endPlay(why, p.remotePosition())
		p.load(p.at)
		return
	}
	for k, i := range p.order {
		if p.queue[i].ID == st.TrackID && p.queue[i].Source == p.track.Source {
			p.endPlay(why, p.remotePosition())
			p.follow(r, k, st)
			return
		}
	}
	p.endPlay(music.EndStopped, p.remotePosition())
	r.pos, r.at = p.remotePositionStopped(r), p.now()
	p.playing = false
	p.err = i18n.T("Playing something else elsewhere")
	p.sync()
}

// follow makes order[at] the current track, which the source already plays.
func (p *Player) follow(r *remote, at int, st *music.RemoteState) {
	p.gen++
	gen := p.gen
	p.at = at
	t := p.queue[p.order[at]]
	p.track = &t
	p.err = ""
	p.lyrics, p.lyricsLoaded = nil, false
	r.seen, r.near, r.polls = true, false, 0
	r.pos, r.at, r.running = time.Duration(st.Position)*time.Millisecond, p.now(), st.Playing
	r.duration = time.Duration(st.Duration) * time.Millisecond
	p.playing = st.Playing
	p.announce(&t, nil)
	p.refill()
	p.loadCover(gen, &t)
	p.loadLyrics(gen, r.src, t)
	p.recorded = ""
	p.record()
	p.listen()
	p.remotePoll(r)
	p.sync()
}
