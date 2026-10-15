package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/go-faster/errors"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func pollMessage(id int, poll tg.Poll, results tg.PollResults) *tg.Message {
	msg := &tg.Message{
		ID:     id,
		PeerID: &tg.PeerChannel{ChannelID: 50},
		Date:   10,
	}
	msg.SetMedia(&tg.MessageMediaPoll{Poll: poll, Results: results})
	return msg
}

func staleResults() tg.PollResults {
	var results tg.PollResults
	results.SetTotalVoters(1)
	row := tg.PollAnswerVoters{Option: []byte("0")}
	row.SetVoters(1)
	results.SetResults([]tg.PollAnswerVoters{row})
	return results
}

func freshResults() tg.PollResults {
	return lunchResults()
}

func TestListHistoryRendersPoll(t *testing.T) {
	poll := lunchPoll(true, true, false, false)
	msg := &tg.Message{ID: 4, PeerID: &tg.PeerUser{UserID: 5}, Date: 4, Message: "see poll"}
	msg.SetMedia(&tg.MessageMediaPoll{Poll: poll, Results: freshResults()})
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		if _, ok := req.(*tg.MessagesGetHistoryRequest); ok {
			return &tg.MessagesMessages{
				Messages: []tg.MessageClass{msg},
				Users:    []tg.UserClass{&tg.User{ID: 5, Username: "alice"}},
			}, nil
		}
		return nil, errors.Errorf("unexpected request %T", req)
	})

	res, err := listHistory(context.Background(), api, &tg.InputPeerSelf{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Media != "poll" || res.Messages[0].Poll == nil {
		t.Fatalf("message = %+v", res.Messages)
	}
	if res.Messages[0].Poll.Question != "Lunch?" || res.Messages[0].Poll.Options[1].Voters != 2 {
		t.Fatalf("poll = %+v", res.Messages[0].Poll)
	}
	var buf bytes.Buffer
	if err := res.MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	if !strings.Contains(text, "[poll]") || !strings.Contains(text, "Lunch?") || !strings.Contains(text, "60.0%") {
		t.Fatalf("history text = %s", text)
	}

	// search/context/pinned share messagesToHistory.
	mapped := messagesToHistory([]tg.MessageClass{msg}, entitiesOf(nil, nil))
	if mapped.Messages[0].Poll == nil || mapped.Messages[0].Poll.Question != "Lunch?" {
		t.Fatalf("mapped poll = %+v", mapped.Messages[0].Poll)
	}
}

func TestShowPollRefreshesResults(t *testing.T) {
	poll := lunchPoll(true, false, false, false)
	var sawResults bool
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.ChannelsGetMessagesRequest:
			ch, ok := r.Channel.(*tg.InputChannel)
			if !ok || ch.ChannelID != 50 {
				t.Errorf("channel = %#v", r.Channel)
			}
			return &tg.MessagesMessages{
				Messages: []tg.MessageClass{pollMessage(7, poll, staleResults())},
				Chats: []tg.ChatClass{&tg.Channel{
					ID: 50, Title: "Ops", Megagroup: true, Photo: &tg.ChatPhotoEmpty{},
				}},
			}, nil
		case *tg.MessagesGetPollResultsRequest:
			sawResults = true
			if r.MsgID != 7 {
				t.Errorf("msg id = %d", r.MsgID)
			}
			up := &tg.UpdateMessagePoll{PollID: poll.ID, Results: freshResults()}
			return &tg.Updates{Updates: []tg.UpdateClass{up}}, nil
		default:
			return nil, errors.Errorf("unexpected request %T", req)
		}
	})

	res, err := showPoll(context.Background(), api, &tg.InputPeerChannel{ChannelID: 50, AccessHash: 9}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !sawResults {
		t.Fatal("messages.getPollResults was not called")
	}
	if res.MessageID != 7 || res.Poll.TotalVoters != 5 || res.Poll.Options[0].Voters != 3 {
		t.Fatalf("stale results were kept: %+v", res.Poll)
	}
	if res.Peer.ID != 50 || res.Peer.Name != "Ops" {
		t.Fatalf("peer = %+v", res.Peer)
	}
}

func TestVotePoll(t *testing.T) {
	poll := lunchPoll(true, true, false, false)
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.MessagesGetMessagesRequest:
			return &tg.MessagesMessages{
				Messages: []tg.MessageClass{pollMessage(7, poll, staleResults())},
			}, nil
		case *tg.MessagesSendVoteRequest:
			if r.MsgID != 7 || len(r.Options) != 2 || string(r.Options[0]) != "0" || string(r.Options[1]) != "1" {
				t.Fatalf("vote request = %+v", r.Options)
			}
			up := &tg.UpdateMessagePoll{PollID: poll.ID, Results: freshResults()}
			return &tg.Updates{Updates: []tg.UpdateClass{up}}, nil
		default:
			return nil, errors.Errorf("unexpected request %T", req)
		}
	})

	// Saved Messages use messages.getMessages, not channels.getMessages.
	peer := &tg.InputPeerSelf{}
	// The sample message is a channel peer, but the request path follows the input peer.
	res, err := votePoll(context.Background(), api, peer, 7, []string{"Pizza", "2"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Poll.Options[0].Chosen || res.Poll.Options[0].Voters != 3 {
		t.Fatalf("updated poll = %+v", res.Poll.Options)
	}
}

func TestVoteRetractAndGuards(t *testing.T) {
	poll := lunchPoll(false, false, false, false)
	var sent bool
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.MessagesGetMessagesRequest:
			return &tg.MessagesMessages{Messages: []tg.MessageClass{pollMessage(3, poll, freshResults())}}, nil
		case *tg.MessagesSendVoteRequest:
			sent = true
			if len(r.Options) != 0 {
				t.Fatalf("retract options = %d, want 0", len(r.Options))
			}
			return &tg.Updates{}, nil
		default:
			return nil, errors.Errorf("unexpected request %T", req)
		}
	})
	peer := &tg.InputPeerUser{UserID: 1}
	if _, err := votePoll(context.Background(), api, peer, 3, nil, true); err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("retract did not call messages.sendVote")
	}
	if _, err := votePoll(context.Background(), api, peer, 3, []string{"1"}, true); err == nil {
		t.Fatal("expected error when retract is combined with options")
	}

	closed := lunchPoll(false, false, false, true)
	closedAPI := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		if _, ok := req.(*tg.MessagesGetMessagesRequest); ok {
			return &tg.MessagesMessages{Messages: []tg.MessageClass{pollMessage(3, closed, freshResults())}}, nil
		}
		return nil, errors.Errorf("unexpected request %T", req)
	})
	if _, err := votePoll(context.Background(), closedAPI, peer, 3, []string{"Pizza"}, false); err == nil {
		t.Fatal("expected closed poll error")
	}
}

func TestListPollVoters(t *testing.T) {
	poll := lunchPoll(true, false, false, false)
	var pages int
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch r := req.(type) {
		case *tg.MessagesGetMessagesRequest:
			return &tg.MessagesMessages{Messages: []tg.MessageClass{pollMessage(8, poll, freshResults())}}, nil
		case *tg.MessagesGetPollVotesRequest:
			pages++
			if string(r.Option) != "0" {
				t.Fatalf("option filter = %q", r.Option)
			}
			if pages == 1 {
				if r.Limit != 2 {
					t.Fatalf("first limit = %d", r.Limit)
				}
				list := &tg.MessagesVotesList{
					Count: 2,
					Votes: []tg.MessagePeerVoteClass{
						&tg.MessagePeerVoteInputOption{Peer: &tg.PeerUser{UserID: 9}, Date: 11},
					},
					Users: []tg.UserClass{&tg.User{ID: 9, FirstName: "Pavel", Username: "durov"}},
				}
				list.SetNextOffset("next")
				return list, nil
			}
			if r.Offset != "next" {
				t.Fatalf("offset = %q", r.Offset)
			}
			return &tg.MessagesVotesList{
				Count: 2,
				Votes: []tg.MessagePeerVoteClass{
					&tg.MessagePeerVote{Peer: &tg.PeerUser{UserID: 4}, Option: []byte("0"), Date: 12},
				},
				Users: []tg.UserClass{&tg.User{ID: 4, FirstName: "Ada"}},
			}, nil
		default:
			return nil, errors.Errorf("unexpected request %T", req)
		}
	})

	res, err := listPollVoters(context.Background(), api, &tg.InputPeerUser{UserID: 1}, 8, "Pizza", 2)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || res.Count != 2 || len(res.Voters) != 2 {
		t.Fatalf("pages=%d count=%d voters=%d", pages, res.Count, len(res.Voters))
	}
	if res.Voters[0].Peer.Username != "durov" || res.Voters[0].Options[0].Text != "Pizza" {
		t.Fatalf("first voter = %+v %+v", res.Voters[0].Peer, res.Voters[0].Options)
	}
	if res.Voters[1].Peer.Name != "Ada" || res.Voters[1].Options[0].Index != 1 {
		t.Fatalf("second voter = %+v %+v", res.Voters[1].Peer, res.Voters[1].Options)
	}
	var buf bytes.Buffer
	if err := res.MarshalText(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Pavel @durov — 1. Pizza") || !strings.Contains(buf.String(), "Ada — 1. Pizza") {
		t.Fatalf("voters text = %s", buf.String())
	}
}

func TestListPollVotersAnonymous(t *testing.T) {
	poll := lunchPoll(false, false, false, false)
	api := newFuncAPI(t, func(req bin.Encoder) (bin.Encoder, error) {
		switch req.(type) {
		case *tg.MessagesGetMessagesRequest:
			return &tg.MessagesMessages{Messages: []tg.MessageClass{pollMessage(8, poll, freshResults())}}, nil
		default:
			return nil, errors.Errorf("unexpected request %T", req)
		}
	})
	_, err := listPollVoters(context.Background(), api, &tg.InputPeerUser{UserID: 1}, 8, "", 10)
	if err == nil || !strings.Contains(err.Error(), "anonymous") {
		t.Fatalf("err = %v", err)
	}
}
