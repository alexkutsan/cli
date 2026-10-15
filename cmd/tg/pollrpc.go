package main

import (
	"context"
	"strings"

	"github.com/go-faster/errors"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

// errAnonymousPoll is returned by `tg poll voters` for anonymous polls.
const errAnonymousPoll = "poll is anonymous; voter lists are only available for public-voter polls"

// maxPollVotePages bounds messages.getPollVotes pagination.
const maxPollVotePages = 50

// pollVotesPage is the per-request cap Telegram accepts for getPollVotes.
const pollVotesPage = 100

func eachUpdate(u tg.UpdatesClass, fn func(tg.UpdateClass) bool) {
	if u == nil {
		return
	}
	var ups []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		ups = v.Updates
	case *tg.UpdatesCombined:
		ups = v.Updates
	case *tg.UpdateShort:
		if v.Update != nil {
			ups = []tg.UpdateClass{v.Update}
		}
	default:
		return
	}
	for _, up := range ups {
		if !fn(up) {
			return
		}
	}
}

// mergePollUpdate overlays a messages.getPollResults or messages.sendVote
// update onto the poll loaded from the message. The message copy is kept when
// the update does not include a full poll.
func mergePollUpdate(poll tg.Poll, results tg.PollResults, u tg.UpdatesClass) (tg.Poll, tg.PollResults) {
	eachUpdate(u, func(up tg.UpdateClass) bool {
		switch v := up.(type) {
		case *tg.UpdateMessagePoll:
			if p, ok := v.GetPoll(); ok && len(p.Answers) > 0 {
				poll = p
			}
			results = v.Results
			return false
		case *tg.UpdateEditMessage:
			msg, ok := v.Message.(*tg.Message)
			if !ok {
				return true
			}
			mp, ok := messagePoll(msg)
			if !ok {
				return true
			}
			poll = mp.Poll
			results = mp.Results
			return false
		default:
			return true
		}
	})
	return poll, results
}

func messagePoll(msg *tg.Message) (*tg.MessageMediaPoll, bool) {
	if msg == nil {
		return nil, false
	}
	media, ok := msg.GetMedia()
	if !ok {
		return nil, false
	}
	mp, ok := media.(*tg.MessageMediaPoll)
	return mp, ok
}

func fetchMessage(ctx context.Context, api *tg.Client, p tg.InputPeerClass, id int) (*tg.Message, peer.Entities, error) {
	res, err := getMessageByID(ctx, api, p, id)
	if err != nil {
		return nil, peer.Entities{}, err
	}
	msgs, ent, err := messagesFrom(res)
	if err != nil {
		return nil, peer.Entities{}, err
	}
	for _, mc := range msgs {
		msg, ok := mc.(*tg.Message)
		if ok && msg.ID == id {
			return msg, ent, nil
		}
	}
	return nil, ent, errors.Errorf("message %d not found", id)
}

func getMessageByID(ctx context.Context, api *tg.Client, p tg.InputPeerClass, id int) (tg.MessagesMessagesClass, error) {
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
	if ch, ok := p.(*tg.InputPeerChannel); ok {
		res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			ID:      ids,
		})
		if err != nil {
			return nil, errors.Wrap(err, "channels.getMessages")
		}
		return res, nil
	}
	res, err := api.MessagesGetMessages(ctx, ids)
	if err != nil {
		return nil, errors.Wrap(err, "messages.getMessages")
	}
	return res, nil
}

func loadPollMessage(
	ctx context.Context,
	api *tg.Client,
	p tg.InputPeerClass,
	msgID int,
) (*tg.Message, *tg.MessageMediaPoll, peer.Entities, error) {
	msg, ent, err := fetchMessage(ctx, api, p, msgID)
	if err != nil {
		return nil, nil, ent, err
	}
	mp, ok := messagePoll(msg)
	if !ok {
		return msg, nil, ent, errors.Errorf("message %d is not a poll", msgID)
	}
	return msg, mp, ent, nil
}

func peerFromMessage(msg *tg.Message, ent peer.Entities, fallback tg.InputPeerClass) peerRef {
	if msg != nil && msg.PeerID != nil {
		ref := describePeer(msg.PeerID, ent)
		if ref.Type != "" && ref.Type != peerUnknown {
			return ref
		}
	}
	return describeInputPeer(fallback)
}

func describeInputPeer(p tg.InputPeerClass) peerRef {
	switch v := p.(type) {
	case *tg.InputPeerSelf:
		return peerRef{Type: peerUser, Name: "me"}
	case *tg.InputPeerUser:
		return peerRef{ID: v.UserID, Type: peerUser}
	case *tg.InputPeerChat:
		return peerRef{ID: v.ChatID, Type: peerChat}
	case *tg.InputPeerChannel:
		return peerRef{ID: v.ChannelID, Type: peerChannel}
	default:
		return peerRef{Type: peerUnknown}
	}
}

func pollMessageFrom(
	msg *tg.Message,
	ent peer.Entities,
	fallback tg.InputPeerClass,
	msgID int,
	poll tg.Poll,
	results tg.PollResults,
) pollMessageResult {
	return pollMessageResult{
		Peer:      peerFromMessage(msg, ent, fallback),
		MessageID: msgID,
		Poll:      buildPollView(poll, results),
	}
}

// showPoll loads the poll message and refreshes results with messages.getPollResults.
func showPoll(ctx context.Context, api *tg.Client, p tg.InputPeerClass, msgID int) (pollMessageResult, error) {
	msg, mp, ent, err := loadPollMessage(ctx, api, p, msgID)
	if err != nil {
		return pollMessageResult{}, err
	}
	ups, err := api.MessagesGetPollResults(ctx, &tg.MessagesGetPollResultsRequest{
		Peer:  p,
		MsgID: msgID,
	})
	if err != nil {
		return pollMessageResult{}, errors.Wrap(err, "messages.getPollResults")
	}
	poll, results := mergePollUpdate(mp.Poll, mp.Results, ups)
	return pollMessageFrom(msg, ent, p, msgID, poll, results), nil
}

// votePoll sends messages.sendVote. choices are 1-based indexes or option text.
// retract sends an empty option list, which removes the caller's vote.
func votePoll(
	ctx context.Context,
	api *tg.Client,
	p tg.InputPeerClass,
	msgID int,
	choices []string,
	retract bool,
) (pollMessageResult, error) {
	if retract && len(choices) > 0 {
		return pollMessageResult{}, errors.New("do not pass options with --retract")
	}
	if !retract && len(choices) == 0 {
		return pollMessageResult{}, errors.New("provide at least one option (1-based index or text), or pass --retract")
	}
	msg, mp, ent, err := loadPollMessage(ctx, api, p, msgID)
	if err != nil {
		return pollMessageResult{}, err
	}
	if mp.Poll.Closed {
		return pollMessageResult{}, errors.New("poll is closed")
	}
	opts := [][]byte{}
	if !retract {
		opts, err = resolvePollChoices(mp.Poll, choices)
		if err != nil {
			return pollMessageResult{}, err
		}
	}
	ups, err := api.MessagesSendVote(ctx, &tg.MessagesSendVoteRequest{
		Peer:    p,
		MsgID:   msgID,
		Options: opts,
	})
	if err != nil {
		return pollMessageResult{}, errors.Wrap(err, "messages.sendVote")
	}
	poll, results := mergePollUpdate(mp.Poll, mp.Results, ups)
	return pollMessageFrom(msg, ent, p, msgID, poll, results), nil
}

// listPollVoters returns public voters via messages.getPollVotes.
// option filters to one answer (1-based index or text) when non-empty.
func listPollVoters(
	ctx context.Context,
	api *tg.Client,
	p tg.InputPeerClass,
	msgID int,
	option string,
	limit int,
) (pollVotersResult, error) {
	if limit < 1 {
		return pollVotersResult{}, errors.New("limit must be positive")
	}
	msg, mp, ent, err := loadPollMessage(ctx, api, p, msgID)
	if err != nil {
		return pollVotersResult{}, err
	}
	if !mp.Poll.PublicVoters {
		return pollVotersResult{}, errors.New(errAnonymousPoll)
	}
	var filter []byte
	if strings.TrimSpace(option) != "" {
		filter, err = resolvePollChoice(mp.Poll, option)
		if err != nil {
			return pollVotersResult{}, err
		}
	}
	voters, total, err := collectPollVoters(ctx, api, p, msgID, mp.Poll, filter, limit)
	if err != nil {
		return pollVotersResult{}, err
	}
	return pollVotersResult{
		Peer:      peerFromMessage(msg, ent, p),
		MessageID: msgID,
		Count:     total,
		Voters:    voters,
	}, nil
}

func collectPollVoters(
	ctx context.Context,
	api *tg.Client,
	p tg.InputPeerClass,
	msgID int,
	poll tg.Poll,
	filter []byte,
	limit int,
) ([]pollVoter, int, error) {
	voters := make([]pollVoter, 0)
	offset := ""
	total := 0
	for page := 0; page < maxPollVotePages && len(voters) < limit; page++ {
		req := &tg.MessagesGetPollVotesRequest{
			Peer:  p,
			ID:    msgID,
			Limit: min(pollVotesPage, limit-len(voters)),
		}
		if filter != nil {
			req.SetOption(filter)
		}
		if offset != "" {
			req.SetOffset(offset)
		}
		res, err := api.MessagesGetPollVotes(ctx, req)
		if err != nil {
			return nil, 0, errors.Wrap(err, "messages.getPollVotes")
		}
		total = res.Count
		batch := entitiesOf(res.Users, res.Chats)
		voters = append(voters, votesToVoters(res.Votes, batch, poll, filter)...)
		next, ok := res.GetNextOffset()
		if !ok || next == "" || next == offset || len(res.Votes) == 0 {
			break
		}
		offset = next
	}
	if len(voters) > limit {
		voters = voters[:limit]
	}
	return voters, total, nil
}

func votesToVoters(votes []tg.MessagePeerVoteClass, ent peer.Entities, poll tg.Poll, filter []byte) []pollVoter {
	out := make([]pollVoter, 0, len(votes))
	for _, v := range votes {
		switch x := v.(type) {
		case *tg.MessagePeerVote:
			out = append(out, pollVoter{
				Peer:    describePeer(x.Peer, ent),
				Options: []pollChoiceRef{choiceRef(poll, x.Option)},
				Date:    x.Date,
			})
		case *tg.MessagePeerVoteMultiple:
			refs := make([]pollChoiceRef, 0, len(x.Options))
			for _, opt := range x.Options {
				refs = append(refs, choiceRef(poll, opt))
			}
			out = append(out, pollVoter{
				Peer:    describePeer(x.Peer, ent),
				Options: refs,
				Date:    x.Date,
			})
		case *tg.MessagePeerVoteInputOption:
			out = append(out, pollVoter{
				Peer:    describePeer(x.Peer, ent),
				Options: []pollChoiceRef{choiceRef(poll, filter)},
				Date:    x.Date,
			})
		}
	}
	return out
}
