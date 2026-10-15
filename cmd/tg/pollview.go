package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-faster/errors"

	"github.com/gotd/td/tg"
)

// pollOptionView is one answer row of a poll, with the caller's vote and the
// current tally. Index is 1-based. Option is the raw option token (the bytes
// Telegram expects in messages.sendVote), rendered as text when they are
// printable and as hex otherwise.
type pollOptionView struct {
	Index   int     `json:"index"`
	Option  string  `json:"option"`
	Text    string  `json:"text"`
	Voters  int     `json:"voters"`
	Percent float64 `json:"percent"`
	Chosen  bool    `json:"chosen"`
	Correct bool    `json:"correct,omitempty"`
}

// pollView is a poll plus its results, shared by history-like output and the
// poll show/vote commands.
type pollView struct {
	ID             int64            `json:"id,omitempty"`
	Question       string           `json:"question"`
	Closed         bool             `json:"closed"`
	MultipleChoice bool             `json:"multiple_choice"`
	Quiz           bool             `json:"quiz"`
	PublicVoters   bool             `json:"public_voters"`
	TotalVoters    int              `json:"total_voters"`
	Chosen         []int            `json:"chosen"`
	Options        []pollOptionView `json:"options"`
}

// pollMessageResult is the result of `tg poll show` and `tg poll vote`.
type pollMessageResult struct {
	Peer      peerRef  `json:"peer"`
	MessageID int      `json:"message_id"`
	Poll      pollView `json:"poll"`
}

// pollChoiceRef identifies one option a voter picked.
type pollChoiceRef struct {
	Index  int    `json:"index,omitempty"`
	Option string `json:"option,omitempty"`
	Text   string `json:"text,omitempty"`
}

// pollVoter is one public voter.
type pollVoter struct {
	Peer    peerRef         `json:"peer"`
	Options []pollChoiceRef `json:"options"`
	Date    int             `json:"date,omitempty"`
}

// pollVotersResult is the result of `tg poll voters`.
type pollVotersResult struct {
	Peer      peerRef     `json:"peer"`
	MessageID int         `json:"message_id"`
	Count     int         `json:"count"`
	Voters    []pollVoter `json:"voters"`
}

// MarshalText renders the poll question, flags, and per-option tallies.
func (v pollView) MarshalText(w io.Writer) error {
	return writePoll(w, v, "")
}

// MarshalText renders a poll message.
func (r pollMessageResult) MarshalText(w io.Writer) error {
	if _, err := fmt.Fprintf(w, "poll #%d in %s\n", r.MessageID, r.Peer.label()); err != nil {
		return err
	}
	return writePoll(w, r.Poll, "")
}

// MarshalText renders public voters, one per line.
func (r pollVotersResult) MarshalText(w io.Writer) error {
	header := fmt.Sprintf("%d voters", r.Count)
	if r.Count > len(r.Voters) {
		header = fmt.Sprintf("showing %d of %d voters", len(r.Voters), r.Count)
	}
	if _, err := fmt.Fprintln(w, header); err != nil {
		return err
	}
	for _, v := range r.Voters {
		opts := make([]string, 0, len(v.Options))
		for _, o := range v.Options {
			if o.Index > 0 {
				opts = append(opts, fmt.Sprintf("%d. %s", o.Index, o.Text))
				continue
			}
			if o.Text != "" {
				opts = append(opts, o.Text)
				continue
			}
			opts = append(opts, o.Option)
		}
		line := voterLabel(v.Peer) + " — " + strings.Join(opts, ", ")
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func writePoll(w io.Writer, v pollView, indent string) error {
	if _, err := fmt.Fprintln(w, indent+v.Question); err != nil {
		return err
	}
	flags := fmt.Sprintf("multiple=%t quiz=%t public=%t closed=%t voters=%d",
		v.MultipleChoice, v.Quiz, v.PublicVoters, v.Closed, v.TotalVoters)
	if _, err := fmt.Fprintln(w, indent+flags); err != nil {
		return err
	}
	for _, o := range v.Options {
		if err := writePollOption(w, indent, o); err != nil {
			return err
		}
	}
	return nil
}

func writePollOption(w io.Writer, indent string, o pollOptionView) error {
	mark := " "
	if o.Chosen {
		mark = "*"
	}
	line := fmt.Sprintf("%s%s %d. %s — %d (%.1f%%) [option %s]",
		indent, mark, o.Index, o.Text, o.Voters, o.Percent, o.Option)
	if o.Correct {
		line += " correct"
	}
	_, err := fmt.Fprintln(w, line)
	return err
}

func voterLabel(p peerRef) string {
	switch {
	case p.Name != "" && p.Username != "":
		return p.Name + " @" + p.Username
	case p.Username != "":
		return "@" + p.Username
	case p.Name != "":
		return p.Name
	default:
		return p.label()
	}
}

type pollResultRow struct {
	Voters  int
	Chosen  bool
	Correct bool
}

// buildPollView joins a poll definition with its results. Percentages are the
// share of total_voters (people), so a multiple-choice poll can show more than
// 100% across options. When the server omits total_voters, the sum of option
// counts is used.
func buildPollView(poll tg.Poll, results tg.PollResults) pollView {
	rows := indexPollResults(results)
	options := make([]pollOptionView, 0, len(poll.Answers))
	chosen := make([]int, 0)
	sum := 0
	for i, ans := range poll.Answers {
		text, opt, ok := pollAnswerParts(ans)
		if !ok {
			continue
		}
		row := rows[string(opt)]
		sum += row.Voters
		view := pollOptionView{
			Index:   i + 1,
			Option:  optionToken(opt),
			Text:    text,
			Voters:  row.Voters,
			Chosen:  row.Chosen,
			Correct: poll.Quiz && row.Correct,
		}
		if view.Chosen {
			chosen = append(chosen, view.Index)
		}
		options = append(options, view)
	}
	total := results.TotalVoters
	if total == 0 {
		total = sum
	}
	for i := range options {
		options[i].Percent = pollPercent(options[i].Voters, total)
	}
	return pollView{
		ID:             poll.ID,
		Question:       poll.Question.Text,
		Closed:         poll.Closed,
		MultipleChoice: poll.MultipleChoice,
		Quiz:           poll.Quiz,
		PublicVoters:   poll.PublicVoters,
		TotalVoters:    total,
		Chosen:         chosen,
		Options:        options,
	}
}

func indexPollResults(results tg.PollResults) map[string]pollResultRow {
	out := make(map[string]pollResultRow, len(results.Results))
	for _, row := range results.Results {
		out[string(row.Option)] = pollResultRow{
			Voters:  row.Voters,
			Chosen:  row.Chosen,
			Correct: row.Correct,
		}
	}
	return out
}

func pollAnswerParts(ans tg.PollAnswerClass) (text string, option []byte, ok bool) {
	a, ok := ans.(*tg.PollAnswer)
	if !ok || a == nil {
		return "", nil, false
	}
	return a.Text.Text, a.Option, true
}

// pollPercent returns voters/total as a percentage rounded to one decimal.
func pollPercent(voters, total int) float64 {
	if voters <= 0 || total <= 0 {
		return 0
	}
	return math.Round((float64(voters)*1000)/float64(total)) / 10
}

// optionToken renders poll option bytes. Telegram usually stores a short
// ASCII token such as "0"; non-printable bytes are hex.
func optionToken(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) && printableOption(b) {
		return string(b)
	}
	return hex.EncodeToString(b)
}

func printableOption(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// resolvePollChoice maps a user argument to option bytes. Exact option text
// wins over a 1-based index, so an answer literally named "1" is not treated
// as the first option. Case-insensitive text is the fallback before indexes.
func resolvePollChoice(poll tg.Poll, arg string) ([]byte, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, errors.New("empty poll option")
	}
	if opt, hits := matchAnswerText(poll, arg, false); hits == 1 {
		return opt, nil
	} else if hits > 1 {
		return nil, errors.Errorf("option %q matches %d answers", arg, hits)
	}
	if opt, hits := matchAnswerText(poll, arg, true); hits == 1 {
		return opt, nil
	} else if hits > 1 {
		return nil, errors.Errorf("option %q matches %d answers", arg, hits)
	}
	n, err := strconv.Atoi(arg)
	if err != nil {
		return nil, errors.Errorf("unknown poll option %q (use a 1-based index or the option text)", arg)
	}
	if n < 1 || n > len(poll.Answers) {
		return nil, errors.Errorf("option index %d out of range (1-%d)", n, len(poll.Answers))
	}
	_, opt, ok := pollAnswerParts(poll.Answers[n-1])
	if !ok {
		return nil, errors.Errorf("option %d has no answer", n)
	}
	return opt, nil
}

func matchAnswerText(poll tg.Poll, arg string, fold bool) ([]byte, int) {
	var found []byte
	hits := 0
	for _, ans := range poll.Answers {
		text, opt, ok := pollAnswerParts(ans)
		if !ok {
			continue
		}
		match := text == arg
		if fold {
			match = strings.EqualFold(text, arg)
		}
		if !match {
			continue
		}
		hits++
		found = opt
	}
	return found, hits
}

func resolvePollChoices(poll tg.Poll, args []string) ([][]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("provide at least one option (1-based index or text), or pass --retract")
	}
	if !poll.MultipleChoice && len(args) > 1 {
		return nil, errors.New("poll is single-choice; pass one option")
	}
	out := make([][]byte, 0, len(args))
	seen := map[string]struct{}{}
	for _, arg := range args {
		opt, err := resolvePollChoice(poll, arg)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[string(opt)]; ok {
			return nil, errors.Errorf("duplicate option %q", strings.TrimSpace(arg))
		}
		seen[string(opt)] = struct{}{}
		out = append(out, opt)
	}
	return out, nil
}

func choiceRef(poll tg.Poll, option []byte) pollChoiceRef {
	ref := pollChoiceRef{Option: optionToken(option)}
	for i, ans := range poll.Answers {
		text, opt, ok := pollAnswerParts(ans)
		if !ok || string(opt) != string(option) {
			continue
		}
		ref.Index = i + 1
		ref.Text = text
		return ref
	}
	return ref
}
