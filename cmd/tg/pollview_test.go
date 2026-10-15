package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func lunchPoll(public, multiple, quiz, closed bool) tg.Poll {
	p := tg.Poll{
		ID:       99,
		Question: tg.TextWithEntities{Text: "Lunch?"},
		Answers: []tg.PollAnswerClass{
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Pizza"}, Option: []byte("0")},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Sushi"}, Option: []byte("1")},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "1"}, Option: []byte{0xff}},
		},
		PublicVoters:   public,
		MultipleChoice: multiple,
		Quiz:           quiz,
		Closed:         closed,
	}
	return p
}

func lunchResults() tg.PollResults {
	var results tg.PollResults
	results.SetTotalVoters(5)
	pizza := tg.PollAnswerVoters{Option: []byte("0"), Chosen: true}
	pizza.SetVoters(3)
	sushi := tg.PollAnswerVoters{Option: []byte("1")}
	sushi.SetVoters(2)
	named := tg.PollAnswerVoters{Option: []byte{0xff}}
	named.SetVoters(0)
	results.SetResults([]tg.PollAnswerVoters{pizza, sushi, named})
	return results
}

func TestBuildPollView(t *testing.T) {
	view := buildPollView(lunchPoll(true, false, false, false), lunchResults())
	if view.Question != "Lunch?" || !view.PublicVoters || view.MultipleChoice || view.Quiz || view.Closed {
		t.Fatalf("flags = %+v", view)
	}
	if view.TotalVoters != 5 || len(view.Chosen) != 1 || view.Chosen[0] != 1 {
		t.Fatalf("totals/chosen = %d %+v", view.TotalVoters, view.Chosen)
	}
	if len(view.Options) != 3 {
		t.Fatalf("options = %d", len(view.Options))
	}
	if view.Options[0].Index != 1 || view.Options[0].Option != "0" || view.Options[0].Text != "Pizza" {
		t.Fatalf("option 0 = %+v", view.Options[0])
	}
	if view.Options[0].Voters != 3 || view.Options[0].Percent != 60 || !view.Options[0].Chosen {
		t.Fatalf("pizza tally = %+v", view.Options[0])
	}
	if view.Options[1].Voters != 2 || view.Options[1].Percent != 40 || view.Options[1].Chosen {
		t.Fatalf("sushi tally = %+v", view.Options[1])
	}
	// Non-printable option bytes are hex, and the text "1" is not index 1.
	if view.Options[2].Text != "1" || view.Options[2].Option != "ff" || view.Options[2].Index != 3 {
		t.Fatalf("third option = %+v", view.Options[2])
	}

	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"question":"Lunch?"`, `"multiple_choice":false`, `"public_voters":true`, `"chosen":[1]`, `"percent":60`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("json missing %s: %s", want, raw)
		}
	}

	var buf bytes.Buffer
	if err := view.MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, want := range []string{"Lunch?", "multiple=false quiz=false public=true closed=false voters=5", "* 1. Pizza — 3 (60.0%) [option 0]", "2. Sushi — 2 (40.0%) [option 1]"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
}

func TestBuildPollViewQuizAndPercent(t *testing.T) {
	poll := lunchPoll(false, false, true, true)
	var results tg.PollResults
	results.SetTotalVoters(3)
	right := tg.PollAnswerVoters{Option: []byte("0"), Correct: true, Chosen: true}
	right.SetVoters(1)
	results.SetResults([]tg.PollAnswerVoters{right})
	view := buildPollView(poll, results)
	if !view.Quiz || !view.Closed || !view.Options[0].Correct {
		t.Fatalf("quiz view = %+v", view)
	}
	if view.Options[0].Percent != 33.3 {
		t.Fatalf("percent = %v", view.Options[0].Percent)
	}
}

func TestResolvePollChoice(t *testing.T) {
	poll := lunchPoll(false, false, false, false)
	opt, err := resolvePollChoice(poll, "1")
	if err != nil {
		t.Fatal(err)
	}
	if string(opt) != string([]byte{0xff}) {
		t.Fatalf("text %q resolved to %q, want the answer named 1", "1", opt)
	}
	opt, err = resolvePollChoice(poll, "2")
	if err != nil {
		t.Fatal(err)
	}
	if string(opt) != "1" {
		t.Fatalf("index 2 = %q, want option 1", opt)
	}
	opt, err = resolvePollChoice(poll, "sushi")
	if err != nil {
		t.Fatal(err)
	}
	if string(opt) != "1" {
		t.Fatalf("case-insensitive = %q", opt)
	}
	if _, err := resolvePollChoice(poll, "9"); err == nil {
		t.Fatal("expected out-of-range error")
	}
	if _, err := resolvePollChoice(poll, "nope"); err == nil {
		t.Fatal("expected unknown option error")
	}

	multi := lunchPoll(true, true, false, false)
	got, err := resolvePollChoices(multi, []string{"Pizza", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got[0]) != "0" || string(got[1]) != "1" {
		t.Fatalf("choices = %q", got)
	}
	if _, err := resolvePollChoices(poll, []string{"1", "2"}); err == nil {
		t.Fatal("single-choice poll should reject two options")
	}
	// "1" is the text of option 3; index 3 is that same option.
	if _, err := resolvePollChoices(multi, []string{"1", "3"}); err == nil {
		t.Fatal("expected duplicate option error")
	}
}
