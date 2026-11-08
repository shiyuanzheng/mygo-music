package plugin

import (
	"strings"
	"testing"
	"time"

	"mygo-music/internal/music"
)

// host.player drives the player only with the permission, and only the player
// the app set.
func TestPlayerNeedsPermissionAndPlayer(t *testing.T) {
	p := load(t, plain, `export function suggest() { host.player.play(); return []; }`)
	if _, err := p.Suggest(""); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Errorf("without the permission: %v", err)
	}
	granted := load(t, `{"id":"x","permissions":{"player":true}}`, `export function suggest() { host.player.play(); return []; }`)
	if _, err := granted.Suggest(""); err == nil || !strings.Contains(err.Error(), "reachable") {
		t.Errorf("without a player: %v", err)
	}
}

// What host.player drives is what the app's player does, and state answers
// how playback is going.
func TestPlayerControl(t *testing.T) {
	var played, paused bool
	var sought int64
	var volume int
	SetPlayerControl(&PlayerControl{
		Play:      func() { played = true },
		Pause:     func() { paused = true },
		Seek:      func(ms int64) { sought = ms },
		SetVolume: func(percent int) { volume = percent },
		State: func() PlayerState {
			return PlayerState{Playing: true, TrackID: "1", Position: 3000, Duration: 200000, Volume: 40}
		},
	})
	defer SetPlayerControl(nil)
	p := load(t, `{"id":"x","permissions":{"player":true}}`, `
export function suggest() {
  host.player.play();
  host.player.pause();
  host.player.seek(1500);
  host.player.setVolume(40);
  return [];
}
export async function search() {
  const st = await host.player.state();
  return { tracks: [{ id: [st.playing, st.trackId, st.position, st.duration, st.volume].join(","), name: "state" }] };
}`)
	if _, err := p.Suggest(""); err != nil {
		t.Fatal(err)
	}
	if !played || !paused || sought != 1500 || volume != 40 {
		t.Errorf("the player heard: play %v, pause %v, seek %v, volume %v", played, paused, sought, volume)
	}
	r, err := p.Search("", music.SearchTracks, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.Tracks[0].ID, "true,1,3000,200000,40"; got != want {
		t.Errorf("state: %q, want %q", got, want)
	}
}

// What the player does reaches a plugin that exports onEvent, in order, and
// starts it if it hadn't: a plugin that only hears needs no pages of its own.
// An ended event says how much was heard and why it ended.
func TestObserve(t *testing.T) {
	p := load(t, plain, `
export function onEvent(ev) { host.storage.set("last", ev); }
export function suggest() {
  const ev = host.storage.get("last");
  return [ev ? [ev.type, String(ev.seconds), String(ev.reason)].join(",") : "none"];
}`)
	p.Observe(music.PlayerEvent{Type: "play", TrackID: "1", Position: 1000})
	p.Observe(music.PlayerEvent{Type: "pause", TrackID: "1", Position: 2000})
	p.Observe(music.PlayerEvent{Type: "ended", TrackID: "1", Seconds: 83.25, Reason: music.EndSkipped})
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := p.Suggest("")
		if err == nil && len(got) == 1 && got[0] == "ended,83.25,skipped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the last event heard was %v, %v", got, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.vm == nil {
		t.Error("the events didn't start the plugin")
	}
}

// What the app tells as it quits, it tells at once, and waits for.
func TestObserveSync(t *testing.T) {
	p := load(t, plain, `
export function onEvent(ev) { host.storage.set("last", ev); }
export function suggest() {
  const ev = host.storage.get("last");
  return [ev ? [ev.type, String(ev.seconds), String(ev.reason)].join(",") : "none"];
}`)
	p.ObserveSync(music.PlayerEvent{Type: "ended", TrackID: "t1", Seconds: 83.25, Reason: music.EndSkipped})
	got, err := p.Suggest("")
	if err != nil || got[0] != "ended,83.25,skipped" {
		t.Fatalf("the plugin heard %v, %v", got, err)
	}
}

// A plugin that doesn't export onEvent isn't started by what it doesn't hear.
func TestObserveSkipsUninterested(t *testing.T) {
	p := load(t, plain, `export function suggest() { return []; }`)
	p.Observe(music.PlayerEvent{Type: "play"})
	time.Sleep(50 * time.Millisecond)
	if p.vm != nil {
		t.Error("started a plugin that exports no onEvent")
	}
}

// Close stops what it hears: what arrives after goes unheard, and doesn't
// start the plugin again.
func TestObserveAfterClose(t *testing.T) {
	p := load(t, plain, `
export function onEvent(ev) { host.storage.set("last", ev); }
export function suggest() {
  const ev = host.storage.get("last");
  return [ev ? ev.type : "none"];
}`)
	p.Close()
	p.Observe(music.PlayerEvent{Type: "play"})
	time.Sleep(50 * time.Millisecond)
	if p.vm != nil {
		t.Error("closed, it started again for what it can't hear")
	}
}
