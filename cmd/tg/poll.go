package main

import (
	"context"
	"strconv"
	"time"

	"github.com/go-faster/errors"
	"github.com/spf13/cobra"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
)

// peerForms documents the peer syntax shared with the rest of the CLI.
const peerForms = "Peers match other commands: me/self, @username, a phone number, a t.me link, or id:<n>."

func (a *app) newPollCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "poll",
		Short:   "Create, read, and vote in polls",
		GroupID: groupMessaging,
		Long: `Commands for creating polls and for reading, voting, and listing voters.

` + peerForms,
	}
	cmd.AddCommand(
		a.newPollCreateCmd(),
		a.newPollShowCmd(),
		a.newPollVoteCmd(),
		a.newPollVotersCmd(),
	)
	return cmd
}

func (a *app) newPollCreateCmd() *cobra.Command {
	var (
		multiple bool
		public   bool
		closeIn  time.Duration
	)

	cmd := &cobra.Command{
		Use:   "create <peer> <question> <option> <option> [option...]",
		Short: "Create a poll",
		Long: `Create a poll with a question and at least two options. By default the
poll is single-choice and anonymous.`,
		Example: `  tg poll create @group "Lunch?" "Pizza" "Sushi" "Salad"
  tg poll create me "Pick" "A" "B" --multiple --public --close-in 1h`,
		Args:              cobra.MinimumNArgs(4),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			peer, question, options := args[0], args[1], args[2:]

			answers := make([]message.PollAnswerOption, 0, len(options))
			for _, o := range options {
				answers = append(answers, message.PollAnswer(o))
			}

			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				sender, m, err := a.sender(api)
				if err != nil {
					return err
				}
				bf, err := builderFor(ctx, m, sender, peer)
				if err != nil {
					return err
				}

				poll := message.Poll(question, answers[0], answers[1], answers[2:]...)
				if multiple {
					poll = poll.MultipleChoice(true)
				}
				if public {
					poll = poll.PublicVoters(true)
				}
				if closeIn > 0 {
					poll = poll.ClosePeriod(closeIn)
				}

				id, err := unpack.MessageID(bf.Media(ctx, poll))
				if err != nil {
					return errors.Wrap(err, "create poll")
				}
				return a.printer.Emit(sentResult{Peer: peer, MessageID: id})
			})
		},
	}

	fs := cmd.Flags()
	fs.BoolVar(&multiple, "multiple", false, "allow multiple answers")
	fs.BoolVar(&public, "public", false, "make votes public (non-anonymous)")
	fs.DurationVar(&closeIn, "close-in", 0, "automatically close the poll after this duration")

	return cmd
}

func (a *app) newPollShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <peer> <message-id>",
		Short: "Show a poll and its current results",
		Long: `Fetch a poll message and refresh its results with messages.getPollResults.
Output includes the question, option tokens and 1-based indexes, flags
(multiple-choice, quiz, public voters, closed), total voters, per-option
counts and percentages, and which option you chose.

` + peerForms,
		Example: `  tg poll show @group 12345
  tg poll show id:2201861038 42 --output json`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := strconv.Atoi(args[1])
			if err != nil {
				return errors.Wrap(err, "message-id must be an integer")
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				peer, err := resolvePeer(ctx, m, args[0])
				if err != nil {
					return err
				}
				res, err := showPoll(ctx, api, peer, msgID)
				if err != nil {
					return err
				}
				return a.printer.Emit(res)
			})
		},
	}
	return cmd
}

func (a *app) newPollVoteCmd() *cobra.Command {
	var retract bool

	cmd := &cobra.Command{
		Use:   "vote <peer> <message-id> [option...]",
		Short: "Vote in a poll, or retract your vote",
		Long: `Vote with messages.sendVote. Each option is a 1-based index or the
option text. Pass several options for a multiple-choice poll. --retract
sends an empty vote and removes yours.

` + peerForms,
		Example: `  tg poll vote @group 12345 1
  tg poll vote @group 12345 Pizza
  tg poll vote @group 12345 1 3
  tg poll vote id:2201861038 42 --retract`,
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := strconv.Atoi(args[1])
			if err != nil {
				return errors.Wrap(err, "message-id must be an integer")
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				peer, err := resolvePeer(ctx, m, args[0])
				if err != nil {
					return err
				}
				res, err := votePoll(ctx, api, peer, msgID, args[2:], retract)
				if err != nil {
					return err
				}
				return a.printer.Emit(res)
			})
		},
	}
	cmd.Flags().BoolVar(&retract, "retract", false, "remove your vote")
	return cmd
}

func (a *app) newPollVotersCmd() *cobra.Command {
	var (
		option string
		limit  int
	)

	cmd := &cobra.Command{
		Use:   "voters <peer> <message-id>",
		Short: "List who voted in a public poll",
		Long: `List voters with messages.getPollVotes. Only public-voter polls
(created with --public) expose individual votes; anonymous polls fail
with a clear error. Each row shows the voter's name and @username and
the option they chose. --option limits the list to one answer, by
1-based index or option text.

` + peerForms,
		Example: `  tg poll voters @group 12345
  tg poll voters @group 12345 --option 2 --limit 20
  tg poll voters id:2201861038 42 --option Pizza`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: peerArgCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			msgID, err := strconv.Atoi(args[1])
			if err != nil {
				return errors.Wrap(err, "message-id must be an integer")
			}
			return a.run(cmd.Context(), runParams{auth: authUser}, func(ctx context.Context, api *tg.Client) error {
				m, err := a.manager(api)
				if err != nil {
					return err
				}
				peer, err := resolvePeer(ctx, m, args[0])
				if err != nil {
					return err
				}
				res, err := listPollVoters(ctx, api, peer, msgID, option, limit)
				if err != nil {
					return err
				}
				return a.printer.Emit(res)
			})
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&option, "option", "", "only voters for this 1-based index or option text")
	fs.IntVarP(&limit, "limit", "n", 100, "maximum number of voters to return")
	return cmd
}
