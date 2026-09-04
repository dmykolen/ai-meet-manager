package store

import (
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"
)

// voice makes a repeatable vector that is close to itself and far from the
// others, which is the only property any of this depends on.
func voice(seed uint64, drift float64) []float32 {
	r := rand.New(rand.NewPCG(seed, 7))
	noise := rand.New(rand.NewPCG(seed+999, 11))
	out := make([]float32, 192)
	for i := range out {
		out[i] = float32(r.NormFloat64() + drift*noise.NormFloat64())
	}
	return out
}

func openDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestARememberedVoiceIsRecognisedInTheNextMeeting(t *testing.T) {
	db := openDB(t)
	if err := db.Remember("Tanya", voice(1, 0)); err != nil {
		t.Fatal(err)
	}
	people, err := db.People()
	if err != nil || len(people) != 1 {
		t.Fatalf("people %+v err %v", people, err)
	}

	// The same person again, recorded a little differently.
	names := Recognise(map[string][]float32{
		"SPEAKER_00": voice(1, 0.25),
		"SPEAKER_01": voice(2, 0),
	}, people)

	if names["SPEAKER_00"] != "Tanya" {
		t.Fatalf("names %v, want SPEAKER_00 recognised as Tanya", names)
	}
	if _, wrong := names["SPEAKER_01"]; wrong {
		t.Fatalf("a stranger was named: %v", names)
	}
}

func TestOnePersonIsNotGivenTwoSeatsAtTheTable(t *testing.T) {
	db := openDB(t)
	if err := db.Remember("Olena", voice(3, 0)); err != nil {
		t.Fatal(err)
	}
	people, _ := db.People()

	// Two labels that both sound like Olena — a diarizer splitting one person
	// in half, which happens. Only the better match may take the name.
	names := Recognise(map[string][]float32{
		"SPEAKER_00": voice(3, 0.2),
		"SPEAKER_01": voice(3, 0.4),
	}, people)

	if len(names) != 1 {
		t.Fatalf("names %v, want exactly one of them named", names)
	}
}

func TestVoiceprintsAreCappedAndTheRedundantOneGoes(t *testing.T) {
	db := openDB(t)
	for i := range Keep + 4 {
		if err := db.Remember("Dmytro", voice(uint64(100+i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	people, _ := db.People()
	if len(people) != 1 {
		t.Fatalf("people %+v, want one", people)
	}
	if got := len(people[0].Voiceprints); got != Keep {
		t.Fatalf("kept %d voiceprints, want %d", got, Keep)
	}
}

func TestVoicesTravelWithARename(t *testing.T) {
	db := openDB(t)
	r, err := db.Add(Recording{Kind: Meeting, Title: "t", Audio: "a.wav"})
	if err != nil {
		t.Fatal(err)
	}
	print := voice(42, 0)
	if err := db.SaveVoices(r.ID, map[string][]float32{"SPEAKER_00": print}); err != nil {
		t.Fatal(err)
	}
	if got := db.VoiceIn(r.ID, "SPEAKER_00"); len(got) == 0 {
		t.Fatal("the voiceprint was not kept")
	}

	if err := db.Rename(r.ID, "SPEAKER_00", "Serhii"); err != nil {
		t.Fatal(err)
	}
	// Renaming again must still find it, which is what moving it under the new
	// label buys.
	if got := db.VoiceIn(r.ID, "Serhii"); len(got) == 0 {
		t.Fatal("the voiceprint did not follow the rename")
	}
	if got := db.VoiceIn(r.ID, "SPEAKER_00"); len(got) != 0 {
		t.Fatal("the old label still has a voiceprint")
	}
}

func TestCosineSeparatesAVoiceFromAStranger(t *testing.T) {
	same := Cosine(voice(7, 0), voice(7, 0.2))
	other := Cosine(voice(7, 0), voice(8, 0))
	if same <= other {
		t.Fatalf("same voice scored %.3f, a stranger %.3f", same, other)
	}
	if math.Abs(Cosine(voice(7, 0), voice(7, 0))-1) > 1e-6 {
		t.Fatal("a voice is not identical to itself")
	}
}
