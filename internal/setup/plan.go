package setup

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/samuelncui/djisimbox/internal/config"
	"github.com/samuelncui/djisimbox/internal/telegram"
)

// routeSpec is one parsed -route option.
type routeSpec struct {
	target string
	chats  []string // empty removes the route
}

// plan records the route decisions: a nil value removes the route.
type plan struct {
	set   map[string][]config.Recipient
	order []string
}

func (p *plan) put(iccid string, recipients []config.Recipient) {
	if _, ok := p.set[iccid]; !ok {
		p.order = append(p.order, iccid)
	}
	p.set[iccid] = recipients
}

func parseRoutes(values []string) ([]routeSpec, error) {
	specs := make([]routeSpec, 0, len(values))
	for _, value := range values {
		// Split on the first "=": phone numbers never contain one.
		target, list, ok := strings.Cut(value, "=")
		target, list = strings.TrimSpace(target), strings.TrimSpace(list)
		if !ok || target == "" || list == "" {
			return nil, fmt.Errorf("invalid route %q; use SIM=CHAT_ID[,CHAT_ID] or SIM=-", value)
		}
		spec := routeSpec{target: target}
		if list != "-" {
			for _, part := range strings.Split(list, ",") {
				id, ok := parseChatID(strings.TrimSpace(part))
				if !ok {
					return nil, fmt.Errorf("invalid chat ID in route for %q", target)
				}
				spec.chats = appendUnique(spec.chats, id)
			}
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// newPlan applies the -route options; later options override earlier ones for the same SIM.
func newPlan(specs []routeSpec, sims []sim) (*plan, error) {
	p := &plan{set: map[string][]config.Recipient{}}
	for _, spec := range specs {
		// Resolve the target to ICCIDs.
		var iccids []string
		switch {
		case spec.target == "all":
			for _, item := range sims {
				iccids = append(iccids, item.ICCID)
			}
		case isICCID(spec.target):
			iccids = []string{spec.target}
		default:
			for _, item := range sims {
				if item.Number == spec.target {
					iccids = append(iccids, item.ICCID)
				}
			}
		}
		if len(iccids) == 0 {
			return nil, fmt.Errorf("route %q matches no connected SIM", spec.target)
		}

		// Record the decision.
		for _, iccid := range iccids {
			p.put(iccid, recipientsFor(spec.chats))
		}
	}
	return p, nil
}

// compose applies the plan to the saved routes; routes of SIMs that are not connected stay as they are.
func compose(saved []config.Route, p *plan, sims []sim) (routes []config.Route, kept int) {
	connected := map[string]bool{}
	for _, item := range sims {
		connected[item.ICCID] = true
	}

	// Walk saved routes in order so a rerun keeps the file stable.
	had := map[string]bool{}
	for _, route := range saved {
		had[route.ICCID] = true
		recipients, decided := p.set[route.ICCID]
		if !decided {
			routes = append(routes, route)
			if !connected[route.ICCID] {
				kept++
			}
			continue
		}
		if len(recipients) > 0 {
			route.Recipients = recipients
			routes = append(routes, route)
		}
	}

	// Append routes for SIMs without a saved route.
	for _, iccid := range p.order {
		if !had[iccid] && len(p.set[iccid]) > 0 {
			routes = append(routes, config.Route{ICCID: iccid, Recipients: p.set[iccid]})
		}
	}
	return routes, kept
}

func isICCID(value string) bool {
	return len(value) >= 18 && len(value) <= 22 && strings.Trim(value, "0123456789") == ""
}

func recipientsFor(ids []string) []config.Recipient {
	var recipients []config.Recipient
	for _, id := range ids {
		recipients = append(recipients, config.Recipient{Bot: defaultBot, ChatID: id})
	}
	return recipients
}

// parseChatID accepts a non-zero signed 64-bit integer and returns its canonical form.
func parseChatID(value string) (string, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id == 0 {
		return "", false
	}
	return strconv.FormatInt(id, 10), true
}

func appendUnique(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}

// parseChoice reads list numbers and chat IDs separated by commas or spaces.
// A positive integer of up to three digits is a list number and must be in range;
// longer or negative integers are chat IDs. bad is the first field not understood.
func parseChoice(line string, chats []telegram.Chat) (ids []string, bad string) {
	fields := strings.FieldsFunc(line, func(char rune) bool {
		return char == ',' || char == '，' || char == ' ' || char == '\t'
	})
	if len(fields) == 0 {
		return nil, line
	}
	for _, field := range fields {
		if strings.Trim(strings.TrimPrefix(field, "-"), "0123456789") != "" {
			return nil, field
		}
		number, err := strconv.ParseInt(field, 10, 64)
		if err != nil || number == 0 {
			return nil, field
		}
		id := strconv.FormatInt(number, 10)
		if number > 0 && len(field) <= 3 {
			if number > int64(len(chats)) {
				return nil, field
			}
			id = strconv.FormatInt(chats[number-1].ID, 10)
		}
		ids = appendUnique(ids, id)
	}
	return ids, ""
}

func (s *session) recipientNames(recipients []config.Recipient, list *chatList) string {
	parts := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		part := recipient.ChatID
		for _, chat := range list.chats {
			if strconv.FormatInt(chat.ID, 10) == recipient.ChatID {
				part = chatLabel(chat)
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// noteUnconfigured tells a non-interactive caller which connected SIMs still have no recipients.
func (s *session) noteUnconfigured(saved []config.Route, p *plan, sims []sim) {
	if !s.opts.Yes {
		return
	}
	for _, item := range sims {
		_, decided := p.set[item.ICCID]
		if decided || slices.ContainsFunc(saved, func(route config.Route) bool { return route.ICCID == item.ICCID }) {
			continue
		}
		s.say(s.t.unconfigured, s.simLabel(item.Number, item.ICCID))
	}
}

func (s *session) summarize(cfg *config.Config, username string, sims []sim, names map[int64]string) {
	// Describe settings, then each SIM with its final recipients.
	s.say("")
	s.say("%s", s.t.summaryTitle)
	s.say("  "+s.t.summaryLanguage, cfg.Language)
	bot := defaultBot
	if username != "" {
		bot = "@" + username
	}
	s.say("  "+s.t.summaryBot, bot)

	listed := map[string]bool{}
	show := func(number, iccid string) {
		listed[iccid] = true
		target := s.t.notForwarded
		for _, route := range cfg.Routes {
			if route.ICCID != iccid {
				continue
			}
			parts := make([]string, 0, len(route.Recipients))
			for _, recipient := range route.Recipients {
				label := recipient.ChatID
				if id, err := strconv.ParseInt(label, 10, 64); err == nil && names[id] != "" {
					label = chatLabel(telegram.Chat{ID: id, Name: names[id]})
				}
				parts = append(parts, label)
			}
			target = strings.Join(parts, ", ")
		}
		s.say("  %s -> %s", s.simLabel(number, iccid), target)
	}
	for _, item := range sims {
		show(item.Number, item.ICCID)
	}
	for _, route := range cfg.Routes {
		if !listed[route.ICCID] {
			show("", route.ICCID)
		}
	}
}

func (s *session) say(format string, args ...any) {
	fmt.Fprintf(s.env.Out, format+"\n", args...)
}

// readLine returns the next input line, or the context error as soon as the context ends.
func (s *session) readLine() (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := s.in.ReadString('\n')
		done <- result{line, err}
	}()

	select {
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	case r := <-done:
		if r.err != nil && r.line == "" {
			if errors.Is(r.err, io.EOF) {
				return "", errors.New("input ended before setup finished")
			}
			return "", fmt.Errorf("read input: %w", r.err)
		}
		return strings.TrimSpace(r.line), nil
	}
}

// secret reads one line without echoing it when the input is a terminal.
func (s *session) secret() (string, error) {
	return withoutEcho(s.env.In, s.env.Out, s.readLine)
}

// confirm asks a yes/no question; Enter selects def.
func (s *session) confirm(prompt string, def bool) (bool, error) {
	for {
		s.say("%s", prompt)
		line, err := s.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(line) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		s.say("%s", s.t.askAgain)
	}
}
