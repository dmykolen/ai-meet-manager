package session

import (
	"testing"
	"time"
)

// feed runs d of frames through the detector and returns every transition it
// produced, so a test can assert on the shape of a whole conversation rather
// than on one frame at a time.
func feed(d *Detector, dur time.Duration, mic, sys bool) []Transition {
	var out []Transition
	for range frames(dur) {
		if t := d.Feed(mic, sys); t != Continue {
			out = append(out, t)
		}
	}
	return out
}

func TestNothingIsRecordedWithoutSustainedSpeech(t *testing.T) {
	d := NewDetector()
	// Nineteen seconds is a phone call from the hallway, not a meeting.
	if got := feed(d, StartSpeech-time.Second, true, true); got != nil {
		t.Fatalf("started on %v of speech: %v", StartSpeech-time.Second, got)
	}
	if on, _ := d.Recording(); on {
		t.Fatal("recording without enough speech")
	}
}

func TestSpeechOnTheSystemChannelIsAMeeting(t *testing.T) {
	d := NewDetector()
	if got := feed(d, StartSpeech, true, true); len(got) != 1 || got[0] != Started {
		t.Fatalf("transitions %v, want one Started", got)
	}
	on, kind := d.Recording()
	if !on || kind != Meeting {
		t.Fatalf("recording=%v kind=%v, want a meeting", on, kind)
	}
	if kind.Endpoint() != "/v1/transcribe-diarize" {
		t.Fatalf("a meeting posts to %s", kind.Endpoint())
	}
}

func TestTalkingToYourselfIsANote(t *testing.T) {
	d := NewDetector()
	feed(d, StartSpeech, true, false) // nobody on the other end
	on, kind := d.Recording()
	if !on || kind != Note {
		t.Fatalf("recording=%v kind=%v, want a note", on, kind)
	}
	if kind.Endpoint() != "/v1/transcribe" {
		t.Fatalf("a note posts to %s, and is not diarized", kind.Endpoint())
	}
}

func TestANotificationChimeDoesNotMakeAMeeting(t *testing.T) {
	// A blip of system audio is a chime, not a second person.
	d := NewDetector()
	for i := range frames(StartSpeech) {
		d.Feed(true, i < frames(MeetingAudio)-10)
	}
	if _, kind := d.Recording(); kind != Note {
		t.Fatalf("kind=%v; a chime is not a conversation", kind)
	}
}

func TestANoteThatGainsASecondVoiceBecomesAMeeting(t *testing.T) {
	// You are thinking aloud and somebody calls you.
	d := NewDetector()
	feed(d, StartSpeech, true, false)
	if _, kind := d.Recording(); kind != Note {
		t.Fatalf("kind=%v at the start, want a note", kind)
	}
	feed(d, 5*time.Second, true, true)
	if _, kind := d.Recording(); kind != Meeting {
		t.Fatalf("kind=%v after a second voice, want a meeting", kind)
	}
}

func TestAMeetingSurvivesAPauseThatWouldEndANote(t *testing.T) {
	d := NewDetector()
	feed(d, StartSpeech, true, true)
	if got := feed(d, QuietNote+10*time.Second, false, false); got != nil {
		t.Fatalf("a meeting ended after %v of quiet: %v", QuietNote, got)
	}
	if on, _ := d.Recording(); !on {
		t.Fatal("people do stop talking during meetings")
	}
}

func TestQuietEndsTheRecording(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sys   bool
		quiet time.Duration
	}{
		{"note", false, QuietNote},
		{"meeting", true, QuietMeeting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDetector()
			feed(d, StartSpeech, true, tc.sys)
			got := feed(d, tc.quiet+time.Second, false, false)
			if len(got) != 1 || got[0] != Ended {
				t.Fatalf("transitions %v, want one Ended", got)
			}
			if on, _ := d.Recording(); on {
				t.Fatal("still recording after the quiet period")
			}
		})
	}
}

func TestALongMeetingRollsToANewFileWithoutAGap(t *testing.T) {
	// Rolling rather than stopping: making the meeting earn its twenty seconds
	// of speech again would punch a hole in it every hour.
	d := NewDetector()
	feed(d, StartSpeech, true, true)
	got := feed(d, RollAfter, true, true)

	if len(got) != 1 || got[0] != Rolled {
		t.Fatalf("transitions %v, want one Rolled", got)
	}
	if on, kind := d.Recording(); !on || kind != Meeting {
		t.Fatalf("recording=%v kind=%v after a roll; it must carry on", on, kind)
	}
	if d.Held() > time.Second {
		t.Fatalf("the new file already holds %v", d.Held())
	}
}

func TestASessionCannotRunForever(t *testing.T) {
	// The runaway case: something the VAD keeps calling speech, all day.
	d := NewDetector()
	feed(d, StartSpeech, true, true)
	got := feed(d, HardCap, true, true)

	if len(got) == 0 || got[len(got)-1] != Ended {
		t.Fatalf("transitions %v, want to end at the hard cap", got)
	}
	if on, _ := d.Recording(); on {
		t.Fatalf("still recording after %v", HardCap)
	}
}

func TestAFinishedRecordingDoesNotImmediatelyStartAnother(t *testing.T) {
	d := NewDetector()
	feed(d, StartSpeech, true, true)
	feed(d, QuietMeeting+time.Second, false, false)

	// One frame of speech must not reopen a recording on the strength of the
	// window that the previous one filled.
	if got := feed(d, time.Second, true, true); got != nil {
		t.Fatalf("restarted on a second of speech: %v", got)
	}
}

func TestQuietIsReportedForTheTray(t *testing.T) {
	d := NewDetector()
	feed(d, StartSpeech, true, true)
	feed(d, 30*time.Second, false, false)

	if got := d.Quiet(); got < 29*time.Second || got > 31*time.Second {
		t.Fatalf("Quiet()=%v, want about 30s", got)
	}
	feed(d, time.Second, true, true)
	if d.Quiet() != 0 {
		t.Fatalf("Quiet()=%v after speech resumed", d.Quiet())
	}
}

func TestRecordNowStartsWithoutWaitingForSpeech(t *testing.T) {
	// The meeting the detector cannot see: everybody in one room, nothing
	// playing through the speakers.
	d := NewDetector()
	d.Force(true)
	if got := d.Feed(false, false); got != Started {
		t.Fatalf("pressing Record now gave %v in a silent room", got)
	}
	if on, kind := d.Recording(); !on || kind != Meeting {
		t.Fatalf("recording=%v kind=%v; a person pressing the button means speakers", on, kind)
	}
}

func TestAForcedRecordingIgnoresSilence(t *testing.T) {
	// An in-person meeting has long pauses, and nobody wants it cut short.
	d := NewDetector()
	d.Force(true)
	d.Feed(false, false)

	if got := feed(d, QuietMeeting+time.Minute, false, false); got != nil {
		t.Fatalf("a forced recording ended on silence: %v", got)
	}
	if on, _ := d.Recording(); !on {
		t.Fatal("still nothing being recorded after the button was pressed")
	}
}

func TestStopEndsAForcedRecording(t *testing.T) {
	d := NewDetector()
	d.Force(true)
	d.Feed(false, false)

	d.Force(false)
	if got := d.Feed(true, true); got != Ended {
		t.Fatalf("pressing Stop gave %v", got)
	}
	if on, _ := d.Recording(); on {
		t.Fatal("still recording after Stop")
	}
}

func TestAForcedRecordingStillCannotRunForEver(t *testing.T) {
	// The button is not a licence to record all day by accident.
	d := NewDetector()
	d.Force(true)
	d.Feed(false, false)

	got := feed(d, HardCap, true, true)
	if len(got) == 0 || got[len(got)-1] != Ended {
		t.Fatalf("transitions %v; the hard cap must still apply", got)
	}
}
