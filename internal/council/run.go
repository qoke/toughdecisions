package council

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/ids"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// ViewSeats are the three parallel seats, in canonical order.
var ViewSeats = []pack.Seat{pack.SeatPossibility, pack.SeatPerspective, pack.SeatStressTester}

// CreateRequest starts one production council run.
type CreateRequest struct {
	ThreadID            string
	Messages            []schema.Message
	Question            string
	Style               string
	SupersedesRequestID string
}

// Runner orchestrates production runs against one DB, gateway, and pack.
type Runner struct {
	db     *store.DB
	gw     gateway.Client
	reg    *Registry
	cfg    *config.Config
	log    logx.Logger
	models *models.Registry
}

// NewRunner builds a Runner. All arguments are required.
func NewRunner(db *store.DB, gw gateway.Client, reg *Registry, cfg *config.Config, log logx.Logger, models *models.Registry) *Runner {
	return &Runner{db: db, gw: gw, reg: reg, cfg: cfg, log: log, models: models}
}

// viewResult is one completed view call delivered to the coordinator.
type viewResult struct {
	seat       pack.Seat
	terminal   bool // complete (usable) as opposed to failed/timed-out
	usable     bool // first-usable accounting: complete with parsed-or-non-empty text
	danger     bool
	caution    string
	resp       *store.Response
	view       *schema.View
	rendered   schema.RenderedView
	parseOK    bool
	failReason string // timeout|error|substituted|unsupported
	timedOut   bool
	errText    string
}

// Run persists the run rows and returns the request id immediately; the
// views and judge continue in a registry-owned goroutine. The goroutine
// context is detached from the caller's context (B1) so returning from the
// HTTP handler cannot cancel in-flight work.
func (r *Runner) Run(ctx context.Context, req CreateRequest) (string, error) {
	if strings.TrimSpace(req.ThreadID) == "" {
		return "", fmt.Errorf("council: thread_id is required")
	}
	if len(req.Messages) == 0 && strings.TrimSpace(req.Question) == "" {
		return "", fmt.Errorf("council: messages or question is required")
	}
	pk, err := pack.Active(r.db)
	if err != nil {
		return "", err
	}
	card, err := r.db.LatestCard(req.ThreadID)
	if err != nil {
		return "", fmt.Errorf("council: latest card: %w", err)
	}
	var cardDoc schema.Card
	if err := json.Unmarshal([]byte(card.CardJSON), &cardDoc); err != nil {
		return "", fmt.Errorf("council: decode card: %w", err)
	}
	input := schema.CaseInput{Card: cardDoc, Messages: req.Messages, Question: req.Question, Style: req.Style}
	snapJSON := hash.CanonicalJSON(input)
	inputHash := hash.SHA256Hex(snapJSON)

	now := time.Now().UTC()
	viewsDeadlineAt := now.Add(r.cfg.ViewsDeadline()).UTC().Format(time.RFC3339)
	requestID := ids.NewID()
	var supersedes *string
	if req.SupersedesRequestID != "" {
		s := req.SupersedesRequestID
		supersedes = &s
	}
	if _, err := r.db.InsertRequest(&store.Request{
		ID: requestID, ThreadID: req.ThreadID, CardID: card.ID, PackID: pk.ID,
		SnapshotJSON: string(snapJSON), InputHash: inputHash, State: "created",
		SupersedesRequestID: supersedes, ViewsDeadlineAt: viewsDeadlineAt,
	}); err != nil {
		return "", err
	}
	viewRowIDs := map[pack.Seat]string{}
	for _, seat := range ViewSeats {
		v, err := r.db.InsertRequestView(requestID, string(seat), "pending")
		if err != nil {
			return "", err
		}
		viewRowIDs[seat] = v.ID
	}
	if _, err := r.db.InsertRequestJudge(requestID); err != nil {
		return "", err
	}

	// Supersede bookkeeping before detaching: the old run keeps its rows.
	if req.SupersedesRequestID != "" {
		oldID := req.SupersedesRequestID
		if serr := r.db.SetSuperseded(oldID, requestID); serr != nil {
			r.log.Error("council supersede", "old_id", oldID, "request_id", requestID, "err", serr)
		}
		r.reg.Cancel(oldID)
		r.reg.Publish(oldID, Event{Type: EventSuperseded, RequestID: oldID, At: time.Now(), Payload: map[string]string{"by": requestID}})
	}

	// B1: detach from the caller's (HTTP request) context. runCancel lives
	// in the registry so a later supersede can cancel this run.
	runCtx, runCancel := context.WithCancel(context.WithoutCancel(ctx))
	r.reg.Register(requestID, runCancel)

	go r.execute(runCtx, runCancel, executeArgs{
		requestID: requestID, pack: pk, input: input, inputHash: inputHash,
		start: now, viewRowIDs: viewRowIDs, threadID: req.ThreadID,
	})
	return requestID, nil
}

type executeArgs struct {
	requestID  string
	pack       *pack.Pack
	input      schema.CaseInput
	inputHash  string
	start      time.Time
	viewRowIDs map[pack.Seat]string
	threadID   string
}

// execute runs views, the deadline-triggered judge, and completion.
func (r *Runner) execute(runCtx context.Context, runCancel context.CancelFunc, a executeArgs) {
	defer runCancel()
	if _, err := r.db.UpdateRequestStateIfNotSuperseded(a.requestID, "running_views"); err != nil {
		r.log.Error("council update state", "request_id", a.requestID, "err", err)
	}

	viewsCtx, viewsCancel := context.WithDeadline(runCtx, a.start.Add(r.cfg.ViewsDeadline()))
	defer viewsCancel()

	results := make(chan viewResult, len(ViewSeats))
	var wg sync.WaitGroup
	for _, seat := range ViewSeats {
		wg.Add(1)
		go func(s pack.Seat) {
			defer wg.Done()
			// Buffer holds all seats, so this never blocks; late views
			// are still delivered after the judge trigger. viewsCtx
			// cancels in-flight gateway calls at the views deadline so
			// a hung model cannot block wg.Wait (and the run) forever.
			results <- r.callView(viewsCtx, a, s)
		}(seat)
	}
	// Close results once every view goroutine has delivered.
	go func() {
		wg.Wait()
		close(results)
	}()

	triggered := false
	var judgeMu sync.Mutex
	included := map[pack.Seat]viewResult{}
	var order []pack.Seat

	markTriggered := func() bool {
		judgeMu.Lock()
		defer judgeMu.Unlock()
		if triggered {
			return false
		}
		triggered = true
		return true
	}

	// Coordinator: single writer for request_views/requests/judge rows.
	pending := len(ViewSeats)
	var judgeDone = make(chan struct{})
	viewsDrained := make(chan struct{})
	var judgeStartedAt time.Time

loop:
	for pending > 0 {
		select {
		case res, ok := <-results:
			if !ok {
				break loop
			}
			pending--
			judgeMu.Lock()
			late := triggered
			judgeMu.Unlock()
			r.recordView(a, res, late)
			if res.terminal && !late {
				included[res.seat] = res
				order = append(order, res.seat)
			}
			if pending == 0 && markTriggered() {
				judgeStartedAt = time.Now()
				go r.runJudgePhase(runCtx, a, included, order, judgeStartedAt, viewsDrained, judgeDone)
				// Late arrivals after all-views trigger: keep draining
				// until every view goroutine has delivered.
				pending = -1
			}
		case <-viewsCtx.Done():
			if markTriggered() {
				judgeStartedAt = time.Now()
				go r.runJudgePhase(runCtx, a, included, order, judgeStartedAt, viewsDrained, judgeDone)
				// Keep draining: views finishing after the deadline are
				// stored late, never re-trigger the judge.
				pending = -1
			}
		case <-runCtx.Done():
			if markTriggered() {
				judgeStartedAt = time.Now()
				go r.runJudgePhase(runCtx, a, included, order, judgeStartedAt, viewsDrained, judgeDone)
				pending = -1
			}
		}
	}
	drained := viewsDrained
	go func() {
		defer close(drained)
		for res := range results {
			r.recordView(a, res, true)
		}
	}()
	// done/judge ordering: wait for every view (including late stragglers)
	// before the judge result is persisted, so done always follows view_late.
	<-drained
	<-judgeDone
}

// recordView persists one view outcome and publishes its event.
func (r *Runner) recordView(a executeArgs, res viewResult, late bool) {
	rowID := a.viewRowIDs[res.seat]
	now := time.Now().UTC().Format(time.RFC3339)
	state := ""
	publishType := ""
	switch {
	case late && res.terminal:
		state = "late"
		publishType = EventViewLate
	case late && res.timedOut:
		// Cancelled by the views deadline but delivered after the judge
		// trigger: still a deadline timeout, not a generic failure.
		state = "timed_out"
		publishType = EventViewFailed
	case late:
		state = "failed"
		publishType = EventViewFailed
	case res.terminal:
		state = "complete"
		publishType = EventViewComplete
	case res.timedOut:
		state = "timed_out"
		publishType = EventViewFailed
	default:
		state = "failed"
		publishType = EventViewFailed
	}
	included := state == "complete"
	if err := r.db.UpdateRequestViewState(rowID, state, respIDOf(res.resp), included, &now); err != nil {
		r.log.Error("council update view", "request_id", a.requestID, "seat", string(res.seat), "err", err)
	}
	if res.resp == nil {
		r.log.Error("council view response missing", "request_id", a.requestID, "seat", string(res.seat), "state", state, "reason", res.failReason, "err", res.errText)
	}
	if res.usable && !late {
		elapsed := time.Since(a.start).Milliseconds()
		if ferr := r.db.SetFirstUsableViewMsIfUnset(a.requestID, elapsed); ferr != nil {
			r.log.Error("council first usable", "request_id", a.requestID, "err", ferr)
		}
	}
	payload := map[string]any{"seat": string(res.seat)}
	if publishType == EventViewFailed {
		payload["reason"] = res.failReason
	}
	if res.danger {
		if derr := r.db.SetDangerFlagged(a.requestID); derr != nil {
			r.log.Error("council set danger", "request_id", a.requestID, "seat", string(res.seat), "err", derr)
		}
		r.reg.Publish(a.requestID, Event{Type: EventDanger, RequestID: a.requestID, At: time.Now(), Payload: map[string]any{"seat": string(res.seat), "caution": res.caution}})
	}
	r.reg.Publish(a.requestID, Event{Type: publishType, RequestID: a.requestID, At: time.Now(), Payload: payload})
}

func respIDOf(resp *store.Response) *string {
	if resp == nil {
		return nil
	}
	id := resp.ID
	return &id
}

// callView performs one view call: validate, build, chat, store, parse.
func (r *Runner) callView(ctx context.Context, a executeArgs, seat pack.Seat) viewResult {
	res := viewResult{seat: seat}
	seatCfg := a.pack.Seats[seat]
	if requiresStrictSchema(r.models, seatCfg.Model) {
		res.failReason = "unsupported"
		return res
	}
	if err := r.models.ValidateSettings(seatCfg); err != nil {
		res.failReason = "unsupported"
		return res
	}
	msgs, err := prompts.BuildViewWithOverride(string(seat), seatCfg.RolePromptOverride, a.input)
	if err != nil {
		res.failReason = "error"
		res.errText = err.Error()
		return res
	}
	gwMsgs := make([]gateway.Message, 0, len(msgs))
	for _, m := range msgs {
		gwMsgs = append(gwMsgs, gateway.Message{Role: m.Role, Content: m.Content})
	}
	t0 := time.Now()
	var chatResp gateway.ChatResponse
	var chatErr error
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				chatErr = fmt.Errorf("council: view panic: %v", rec)
			}
		}()
		chatResp, chatErr = r.gw.Chat(ctx, gateway.ChatRequest{
			Model: seatCfg.Model, Messages: gwMsgs,
			Temperature: seatCfg.Temperature, TopP: seatCfg.TopP,
			MaxOutputTokens: seatCfg.MaxOutputTokens, ReasoningEffort: seatCfg.ReasoningEffort,
			ResponseFormat:        r.responseFormatFor(seatCfg.Model, "view", schema.ViewJSONSchema),
			ExpectedModelPrefixes: r.models.ExpectedPrefixes(seatCfg.Model),
			Tags:                  map[string]string{"request_id": a.requestID, "seat": string(seat)},
		})
	}()
	latency := time.Since(t0).Milliseconds()

	substituted := errors.Is(chatErr, gateway.ErrSubstituted)
	var subResp *gateway.ChatResponse
	if substituted {
		var subErr *gateway.SubstitutionError
		if errors.As(chatErr, &subErr) {
			sr := subErr.Response
			subResp = &sr
		}
	}
	timedOut := errors.Is(chatErr, gateway.ErrTimeout) || errors.Is(chatErr, context.DeadlineExceeded)
	canceled := errors.Is(chatErr, context.Canceled)

	content := chatResp.Content
	returned := chatResp.ModelReturned
	if subResp != nil {
		content = subResp.Content
		returned = subResp.ModelReturned
	}
	// The scripted fake does not enforce substitution; the runner checks
	// expected prefixes itself so a wrong model is always a failure.
	if chatErr == nil && !matchesPrefix(returned, r.models.ExpectedPrefixes(seatCfg.Model)) {
		chatErr = &gateway.SubstitutionError{
			Response: gateway.ChatResponse{ModelReturned: returned, Content: content},
			Returned: returned,
		}
		substituted = true
		sub := chatErr.(*gateway.SubstitutionError)
		sr := sub.Response
		subResp = &sr
		content = subResp.Content
		returned = subResp.ModelReturned
	}
	if chatErr != nil && !substituted {
		if timedOut || canceled {
			respRow := &store.Response{
				CacheKey: ids.NewID(), Seat: string(seat), ConfigHash: seatCfg.Hash(),
				PromptPackHash: a.pack.PromptPackHash, InputHash: a.inputHash,
				Origin: "production", RequestID: &a.requestID,
				ModelRequested: seatCfg.Model, RawText: "", ParseOK: false,
				TimedOut: timedOut, LatencyMs: latency,
				Error: errPtr(chatErr.Error()), WordCount: 0,
			}
			stored, insertErr := r.db.InsertResponse(respRow)
			if insertErr != nil {
				r.log.Error("council store view response", "request_id", a.requestID, "seat", string(seat), "err", insertErr)
			} else {
				res.resp = stored
			}
			res.timedOut = timedOut
			if timedOut {
				res.failReason = "timeout"
			} else {
				res.failReason = "error"
			}
			res.errText = chatErr.Error()
			return res
		}
		respRow := &store.Response{
			CacheKey: ids.NewID(), Seat: string(seat), ConfigHash: seatCfg.Hash(),
			PromptPackHash: a.pack.PromptPackHash, InputHash: a.inputHash,
			Origin: "production", RequestID: &a.requestID,
			ModelRequested: seatCfg.Model, RawText: "", ParseOK: false,
			LatencyMs: latency, Error: errPtr(chatErr.Error()),
		}
		stored, insertErr := r.db.InsertResponse(respRow)
		if insertErr != nil {
			r.log.Error("council store view response", "request_id", a.requestID, "seat", string(seat), "err", insertErr)
		} else {
			res.resp = stored
		}
		res.failReason = "error"
		res.errText = chatErr.Error()
		return res
	}
	if substituted {
		respRow := &store.Response{
			CacheKey: ids.NewID(), Seat: string(seat), ConfigHash: seatCfg.Hash(),
			PromptPackHash: a.pack.PromptPackHash, InputHash: a.inputHash,
			Origin: "production", RequestID: &a.requestID,
			ModelRequested: seatCfg.Model, ModelReturned: returned, Substituted: true,
			RawText: content, ParseOK: false, LatencyMs: latency,
			Error: errPtr("substituted model: " + returned),
		}
		stored, insertErr := r.db.InsertResponse(respRow)
		if insertErr != nil {
			r.log.Error("council store view response", "request_id", a.requestID, "seat", string(seat), "err", insertErr)
		} else {
			res.resp = stored
		}
		res.failReason = "substituted"
		return res
	}

	view, parseOK := schema.ParseLenient[schema.View](content)
	var parsedJSON *string
	if parseOK {
		raw2, _ := json.Marshal(view)
		s := string(raw2)
		parsedJSON = &s
	}
	usable := parseOK || strings.TrimSpace(content) != ""
	rendered := schema.RenderedView{Role: string(seat)}
	if parseOK {
		rendered = schema.RenderedView{
			Role: string(seat), Qualification: view.Qualification,
			SuggestedReply:  firstNonEmpty(view.SuggestedReply, view.RecommendedMove),
			DecisiveInsight: view.DecisiveInsight, TradeoffOrObjection: view.TradeoffOrObjection,
			DependsOn: view.DependsOn, Fallback: view.Fallback, UrgentDanger: view.UrgentDanger,
		}
	}
	respRow := &store.Response{
		CacheKey: ids.NewID(), Seat: string(seat), ConfigHash: seatCfg.Hash(),
		PromptPackHash: a.pack.PromptPackHash, InputHash: a.inputHash,
		Origin: "production", RequestID: &a.requestID,
		ModelRequested: seatCfg.Model, ModelReturned: returned,
		RawText: content, ParsedJSON: parsedJSON, ParseOK: parseOK,
		PromptTokens: chatResp.PromptTokens, CompletionTokens: chatResp.CompletionTokens,
		CostUSD: chatResp.CostUSD, LatencyMs: latency,
		WordCount: len(strings.Fields(content)),
	}
	stored, insertErr := r.db.InsertResponse(respRow)
	if insertErr != nil {
		res.failReason = "error"
		res.errText = insertErr.Error()
		return res
	}
	res.resp = stored
	res.terminal = usable
	res.usable = usable
	res.parseOK = parseOK
	res.view = &view
	res.rendered = rendered
	if parseOK && view.UrgentDanger.Present {
		res.danger = true
		res.caution = view.UrgentDanger.Caution
	}
	if !usable {
		res.failReason = "error"
		res.errText = "empty view output"
	}
	return res
}

// runJudgePhase runs exactly one judge call over the included views.
// viewsDone gates persistence of the result: the judge's DB writes and
// done event happen only after late stragglers are stored, so done always
// follows view_late.
func (r *Runner) runJudgePhase(runCtx context.Context, a executeArgs, included map[pack.Seat]viewResult, order []pack.Seat, startedAt time.Time, viewsDone <-chan struct{}, done chan struct{}) {
	defer close(done)
	startedStr := startedAt.UTC().Format(time.RFC3339)
	deadlineStr := startedAt.Add(r.cfg.JudgeDeadline()).UTC().Format(time.RFC3339)
	if perr := r.db.UpdateRequestProgress(a.requestID, store.RequestProgressPatch{
		State: strPtr("running_judge"), JudgeStartedAt: &startedStr, JudgeDeadlineAt: &deadlineStr,
	}); perr != nil {
		r.log.Error("council judge progress", "request_id", a.requestID, "err", perr)
	}
	r.reg.Publish(a.requestID, Event{Type: EventJudgeStarted, RequestID: a.requestID, At: time.Now(), Payload: map[string]any{"included": seatNames(order)}})

	views := map[string]schema.RenderedView{}
	for seat, res := range included {
		views[string(seat)] = res.rendered
	}
	includedSet := map[string]bool{}
	for s := range views {
		includedSet[s] = true
	}
	var missing []string
	for _, s := range ViewSeats {
		if !includedSet[string(s)] {
			missing = append(missing, string(s))
		}
	}
	sort.Strings(missing)
	noViews := len(views) == 0

	seatCfg := a.pack.Seats[pack.SeatJudge]
	if requiresStrictSchema(r.models, seatCfg.Model) {
		<-viewsDone
		r.finishJudge(a, startedAt, missing, noViews, nil, false, false)
		return
	}
	if err := r.models.ValidateSettings(seatCfg); err != nil {
		<-viewsDone
		r.finishJudge(a, startedAt, missing, noViews, nil, false, false)
		return
	}
	msgs, err := prompts.BuildJudgeWithOverride(a.input, views, missing, noViews, seatCfg.RolePromptOverride)
	if err != nil {
		<-viewsDone
		r.finishJudge(a, startedAt, missing, noViews, nil, false, false)
		return
	}
	gwMsgs := make([]gateway.Message, 0, len(msgs))
	for _, m := range msgs {
		gwMsgs = append(gwMsgs, gateway.Message{Role: m.Role, Content: m.Content})
	}
	judgeCtx, cancel := context.WithDeadline(runCtx, startedAt.Add(r.cfg.JudgeDeadline()))
	defer cancel()
	t0 := time.Now()
	chatResp, chatErr := r.gw.Chat(judgeCtx, gateway.ChatRequest{
		Model: seatCfg.Model, Messages: gwMsgs,
		Temperature: seatCfg.Temperature, TopP: seatCfg.TopP,
		MaxOutputTokens: seatCfg.MaxOutputTokens, ReasoningEffort: seatCfg.ReasoningEffort,
		ResponseFormat:        r.responseFormatFor(seatCfg.Model, "judge", schema.JudgeJSONSchema),
		ExpectedModelPrefixes: r.models.ExpectedPrefixes(seatCfg.Model),
		Tags:                  map[string]string{"request_id": a.requestID, "seat": "judge"},
	})
	latency := time.Since(t0).Milliseconds()
	<-viewsDone
	if chatErr != nil {
		timedOut := errors.Is(chatErr, gateway.ErrTimeout) || errors.Is(chatErr, context.DeadlineExceeded)
		respRow := &store.Response{
			CacheKey: ids.NewID(), Seat: "judge", ConfigHash: seatCfg.Hash(),
			PromptPackHash: a.pack.PromptPackHash, InputHash: a.inputHash,
			Origin: "production", RequestID: &a.requestID,
			ModelRequested: seatCfg.Model, RawText: "", ParseOK: false,
			TimedOut: timedOut, LatencyMs: latency, Error: errPtr(chatErr.Error()),
		}
		stored, insertErr := r.db.InsertResponse(respRow)
		if insertErr != nil {
			r.log.Error("council store judge response", "request_id", a.requestID, "err", insertErr)
			stored = nil
		}
		r.finishJudge(a, startedAt, missing, noViews, stored, false, timedOut)
		return
	}
	judge, parseOK := schema.ParseLenient[schema.Judge](chatResp.Content)
	var parsedJSON *string
	if parseOK {
		raw2, _ := json.Marshal(judge)
		s := string(raw2)
		parsedJSON = &s
	}
	if parseOK && judge.UrgentDanger.Present {
		if derr := r.db.SetDangerFlagged(a.requestID); derr != nil {
			r.log.Error("council set danger", "request_id", a.requestID, "seat", "judge", "err", derr)
		}
		r.reg.Publish(a.requestID, Event{Type: EventDanger, RequestID: a.requestID, At: time.Now(), Payload: map[string]any{"seat": "judge", "caution": judge.UrgentDanger.Caution}})
	}
	respRow := &store.Response{
		CacheKey: ids.NewID(), Seat: "judge", ConfigHash: seatCfg.Hash(),
		PromptPackHash: a.pack.PromptPackHash, InputHash: a.inputHash,
		Origin: "production", RequestID: &a.requestID,
		ModelRequested: seatCfg.Model, ModelReturned: chatResp.ModelReturned,
		RawText: chatResp.Content, ParsedJSON: parsedJSON, ParseOK: parseOK,
		PromptTokens: chatResp.PromptTokens, CompletionTokens: chatResp.CompletionTokens,
		CostUSD: chatResp.CostUSD, LatencyMs: latency,
		WordCount: len(strings.Fields(chatResp.Content)),
	}
	stored, insertErr := r.db.InsertResponse(respRow)
	if insertErr != nil {
		r.log.Error("council store judge response", "request_id", a.requestID, "err", insertErr)
		r.finishJudge(a, startedAt, missing, noViews, nil, false, false)
		return
	}
	r.finishJudge(a, startedAt, missing, noViews, stored, true, false)
}

// finishJudge persists the judge outcome, final state, timings, and events.
// Late views are awaited first: the coordinator drains stragglers before
// done so a "late" arrival is always stored when the run completes.
func (r *Runner) finishJudge(a executeArgs, startedAt time.Time, missing []string, noViews bool, resp *store.Response, ok bool, timedOut bool) {
	included := []string{}
	for _, s := range ViewSeats {
		found := false
		for _, m := range missing {
			if m == string(s) {
				found = true
				break
			}
		}
		if !found {
			included = append(included, string(s))
		}
	}
	sort.Strings(included)
	state := "complete"
	judgeState := "complete"
	eventType := EventJudgeComplete
	if !ok {
		judgeState = "failed"
		if timedOut {
			judgeState = "timed_out"
		}
		eventType = EventJudgeFailed
		state = "complete_partial"
	} else if len(missing) > 0 {
		state = "complete_partial"
	}
	judgeInDeadline := 0
	if time.Since(startedAt) <= r.cfg.JudgeDeadline() && ok {
		judgeInDeadline = 1
	}
	if jerr := r.db.UpdateJudgeResult(a.requestID, store.JudgeResult{
		State: judgeState, ResponseID: respIDOf(resp),
		IncludedSeats: included, MissingSeats: missing, NoIndependentViews: noViews,
	}); jerr != nil {
		r.log.Error("council judge result", "request_id", a.requestID, "err", jerr)
	}
	r.reg.Publish(a.requestID, Event{Type: eventType, RequestID: a.requestID, At: time.Now(), Payload: map[string]any{"included": included, "missing": missing}})

	agg, err := r.db.GetRequest(a.requestID)
	if err != nil {
		r.log.Error("council get request", "request_id", a.requestID, "err", err)
		return
	}
	completedInDeadline := 0
	for _, v := range agg.Views {
		if (v.State == "complete") && v.CompletedAt != nil {
			if ct, err := time.Parse(time.RFC3339, *v.CompletedAt); err == nil {
				if !ct.After(a.start.Add(r.cfg.ViewsDeadline())) {
					completedInDeadline++
				}
			}
		}
	}
	finished := time.Now().UTC().Format(time.RFC3339)
	tFinal := time.Since(a.start).Milliseconds()
	patch := store.RequestProgressPatch{
		FinishedAt: &finished, TFinalMs: &tFinal,
		ViewsCompletedInDeadline: &completedInDeadline,
		JudgeCompletedInDeadline: &judgeInDeadline,
	}
	if updated, err := r.db.UpdateRequestStateIfNotSuperseded(a.requestID, state); err != nil {
		r.log.Error("council finish state", "request_id", a.requestID, "err", err)
	} else if updated {
		if perr := r.db.UpdateRequestProgress(a.requestID, patch); perr != nil {
			r.log.Error("council finish progress", "request_id", a.requestID, "err", perr)
		}
	} else {
		// Superseded runs keep state=superseded but still record timings.
		if perr := r.db.UpdateRequestProgress(a.requestID, patch); perr != nil {
			r.log.Error("council finish progress", "request_id", a.requestID, "err", perr)
		}
	}
	r.reg.Publish(a.requestID, Event{Type: EventDone, RequestID: a.requestID, At: time.Now(), Payload: map[string]any{"state": state}})
	r.reg.MarkDone(a.requestID)
}

// Rewrite performs one rewrite call with the rewrite seat config.
func (r *Runner) Rewrite(ctx context.Context, requestID, sourceRef, instruction string) (string, string, error) {
	if strings.TrimSpace(requestID) == "" {
		return "", "", fmt.Errorf("council: request_id is required")
	}
	if strings.TrimSpace(instruction) == "" {
		return "", "", fmt.Errorf("council: instruction is required")
	}
	agg, err := r.db.GetRequest(requestID)
	if err != nil {
		return "", "", err
	}
	draft, err := r.resolveDraft(agg, sourceRef)
	if err != nil {
		return "", "", err
	}
	pk, err := pack.Active(r.db)
	if err != nil {
		return "", "", err
	}
	seatName := r.cfg.RewriteSeat()
	seat := pack.Seat(seatName)
	if !seat.Valid() {
		seat = pack.SeatJudge
	}
	seatCfg, ok := pk.Seats[seat]
	if !ok {
		return "", "", fmt.Errorf("council: rewrite seat %q not in pack", seatName)
	}
	if err := r.models.ValidateSettings(seatCfg); err != nil {
		return "", "", err
	}
	var input schema.CaseInput
	if err := json.Unmarshal([]byte(agg.Request.SnapshotJSON), &input); err != nil {
		return "", "", fmt.Errorf("council: decode snapshot: %w", err)
	}
	msgs, err := prompts.BuildRewrite(draft, instruction, input)
	if err != nil {
		return "", "", err
	}
	gwMsgs := make([]gateway.Message, 0, len(msgs))
	for _, m := range msgs {
		gwMsgs = append(gwMsgs, gateway.Message{Role: m.Role, Content: m.Content})
	}
	rwCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.cfg.RewriteDeadline()))
	defer cancel()
	chatResp, err := r.gw.Chat(rwCtx, gateway.ChatRequest{
		Model: seatCfg.Model, Messages: gwMsgs,
		Temperature: seatCfg.Temperature, TopP: seatCfg.TopP,
		MaxOutputTokens: seatCfg.MaxOutputTokens, ReasoningEffort: seatCfg.ReasoningEffort,
		ExpectedModelPrefixes: r.models.ExpectedPrefixes(seatCfg.Model),
		Tags:                  map[string]string{"request_id": requestID, "seat": "rewrite"},
	})
	if err != nil {
		return "", "", err
	}
	respRow := &store.Response{
		CacheKey: ids.NewID(), Seat: "rewrite", ConfigHash: seatCfg.Hash(),
		PromptPackHash: pk.PromptPackHash, InputHash: agg.Request.InputHash,
		Origin: "production", RequestID: &requestID,
		ModelRequested: seatCfg.Model, ModelReturned: chatResp.ModelReturned,
		RawText: chatResp.Content, ParseOK: false,
		PromptTokens: chatResp.PromptTokens, CompletionTokens: chatResp.CompletionTokens,
		CostUSD: chatResp.CostUSD, LatencyMs: chatResp.LatencyMs,
		WordCount: len(strings.Fields(chatResp.Content)),
	}
	stored, err := r.db.InsertResponse(respRow)
	if err != nil {
		return "", "", err
	}
	rw, err := r.db.InsertRewrite(requestID, sourceRef, instruction, stored.ID, chatResp.Content)
	if err != nil {
		return "", "", err
	}
	return chatResp.Content, rw.ID, nil
}

// resolveDraft finds the draft text for a source ref:
// "view:<seat>", "judge", or "rewrite:<id>".
func (r *Runner) resolveDraft(agg *store.RequestAggregate, sourceRef string) (string, error) {
	if strings.HasPrefix(sourceRef, "view:") {
		seat := strings.TrimPrefix(sourceRef, "view:")
		for _, v := range agg.Views {
			if v.Seat == seat && v.ResponseID != nil {
				resp, err := r.db.GetResponse(*v.ResponseID)
				if err != nil {
					return "", err
				}
				return resp.RawText, nil
			}
		}
		return "", fmt.Errorf("council: no view response for %q", sourceRef)
	}
	if sourceRef == "judge" {
		if agg.Judge == nil || agg.Judge.ResponseID == nil {
			return "", fmt.Errorf("council: no judge response for request")
		}
		resp, err := r.db.GetResponse(*agg.Judge.ResponseID)
		if err != nil {
			return "", err
		}
		return resp.RawText, nil
	}
	if strings.HasPrefix(sourceRef, "rewrite:") {
		id := strings.TrimPrefix(sourceRef, "rewrite:")
		for _, rw := range agg.Rewrites {
			if rw.ID == id {
				return rw.OutputText, nil
			}
		}
		return "", fmt.Errorf("council: no rewrite %q", id)
	}
	return "", fmt.Errorf("council: unknown source_ref %q", sourceRef)
}

// requiresStrictSchema reports whether the seat model lacks strict
// json_schema support. A nil registry or unknown model fails closed.
func requiresStrictSchema(mreg *models.Registry, model string) bool {
	if mreg == nil {
		return true
	}
	_, _, _, jsonSchema, _ := mreg.Supports(model)
	return !jsonSchema
}

// responseFormatFor selects strict json_schema when the model supports it,
// else nil. There is intentionally no json_object fallback: an unenforced
// view/judge shape is a degraded result, so callers fail closed before any
// gateway call. Rewrites return free text, so only view/judge calls pass a
// schema.
func (r *Runner) responseFormatFor(model, schemaName, schemaJSON string) *gateway.ResponseFormat {
	if r.models == nil {
		return nil
	}
	_, _, _, jsonSchema, _ := r.models.Supports(model)
	if !jsonSchema {
		return nil
	}
	return &gateway.ResponseFormat{
		Type: "json_schema", SchemaName: schemaName,
		Schema: json.RawMessage(schemaJSON), Strict: true,
	}
}

func matchesPrefix(value string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	lower := strings.ToLower(value)
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func seatNames(seats []pack.Seat) []string {
	out := make([]string, 0, len(seats))
	for _, s := range seats {
		out = append(out, string(s))
	}
	return out
}

func strPtr(s string) *string { return &s }

func errPtr(s string) *string { return &s }

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
