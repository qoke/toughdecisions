// Package httpapi exposes the production HTTP API: threads, requests,
// rewrites, sent messages, feedback, pack, metrics, health, and the
// embedded single-page UI. Errors are always {error, code} JSON.
package httpapi

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/council"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

//go:embed ui/index.html
var uiHTML []byte

// MaxBodyBytes caps JSON request bodies (plan §9).
const MaxBodyBytes = 256 * 1024

// SSEHeartbeatInterval is the SSE comment heartbeat period.
const SSEHeartbeatInterval = 15 * time.Second

// SSEDoneGracePeriod keeps an SSE stream open briefly after done so the
// client reliably receives the terminal frame.
const SSEDoneGracePeriod = 2 * time.Second

// validFeedbackTags are the feedback.tag values from plan §4.
var validFeedbackTags = map[string]bool{
	"missed_constraint": true, "invented_motive": true, "wrong_tone": true,
	"unhelpful_repetition": true, "too_slow": true, "other": true,
}

// Server serves the production API over one DB, runner, registry, and config.
type Server struct {
	db     *store.DB
	runner *council.Runner
	reg    *council.Registry
	cfg    *config.Config
	log    logx.Logger
}

// New builds a Server. All arguments are required except runner, which may
// be nil for read-only servers (live POST /api/requests is rejected then).
func New(db *store.DB, runner *council.Runner, reg *council.Registry, cfg *config.Config, log logx.Logger) *Server {
	return &Server{db: db, runner: runner, reg: reg, cfg: cfg, log: log}
}

// Handler returns the mux with every API route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/threads", s.handleCreateThread)
	mux.HandleFunc("GET /api/threads", s.handleListThreads)
	mux.HandleFunc("PUT /api/threads/{id}/card", s.handlePutCard)
	mux.HandleFunc("GET /api/threads/{id}", s.handleGetThread)
	mux.HandleFunc("POST /api/requests", s.handleCreateRequest)
	mux.HandleFunc("GET /api/requests/{id}", s.handleGetRequest)
	mux.HandleFunc("GET /api/requests/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /api/requests/{id}/rewrite", s.handleRewrite)
	mux.HandleFunc("POST /api/requests/{id}/sent", s.handleSent)
	mux.HandleFunc("POST /api/requests/{id}/feedback", s.handleFeedback)
	mux.HandleFunc("GET /api/pack/active", s.handlePackActive)
	mux.HandleFunc("GET /api/metrics/summary", s.handleMetricsSummary)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	s.log.Warn("http error", "method", r.Method, "path", r.URL.Path, "status", status, "code", code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			s.writeErr(w, r, http.StatusRequestEntityTooLarge, "body_too_large", "body exceeds 256 KB")
			return false
		}
		s.writeErr(w, r, http.StatusBadRequest, "bad_json", "invalid JSON body")
		return false
	}
	return true
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(uiHTML)
}

func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "name is required")
		return
	}
	th, err := s.db.CreateThread(body.Name)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "create thread failed")
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{
		"id": th.ID, "created_at": th.CreatedAt, "name": th.Name, "archived": th.Archived,
	})
}

func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListThreads()
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "list threads failed")
		return
	}
	out := make([]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"id": t.ID, "created_at": t.CreatedAt, "name": t.Name, "archived": t.Archived,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"threads": out})
}

func (s *Server) findThread(id string) (*store.Thread, bool) {
	list, err := s.db.ListThreads()
	if err != nil {
		return nil, false
	}
	for _, t := range list {
		if t.ID == id {
			return t, true
		}
	}
	return nil, false
}

func (s *Server) handlePutCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.findThread(id); !ok {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "thread not found")
		return
	}
	var card schema.Card
	if !s.decodeBody(w, r, &card) {
		return
	}
	raw, err := json.Marshal(card)
	if err != nil {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "invalid card")
		return
	}
	c, err := s.db.InsertCard(id, string(raw))
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "save card failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id": c.ID, "thread_id": c.ThreadID, "version": c.Version,
		"card": card, "card_hash": c.CardHash,
	})
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	th, ok := s.findThread(id)
	if !ok {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "thread not found")
		return
	}
	var cardAny any
	latest, err := s.db.LatestCard(id)
	if err == nil {
		var c map[string]any
		if jerr := json.Unmarshal([]byte(latest.CardJSON), &c); jerr == nil {
			cardAny = c
		}
	}
	reqs, err := s.db.ListRequestsByThread(id)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "list requests failed")
		return
	}
	items := make([]any, 0, len(reqs))
	for _, q := range reqs {
		items = append(items, map[string]any{"id": q.ID, "state": q.State, "created_at": q.CreatedAt})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id": th.ID, "name": th.Name, "created_at": th.CreatedAt,
		"card": cardAny, "latest_card": cardAny, "requests": items,
	})
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		s.writeErr(w, r, http.StatusServiceUnavailable, "no_runner", "request runner unavailable")
		return
	}
	var body struct {
		ThreadID            string           `json:"thread_id"`
		Messages            []schema.Message `json:"messages"`
		Question            string           `json:"question"`
		Style               string           `json:"style"`
		SupersedesRequestID string           `json:"supersedes_request_id"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ThreadID) == "" {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "thread_id is required")
		return
	}
	if _, ok := s.findThread(body.ThreadID); !ok {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "thread not found")
		return
	}
	empty := len(body.Messages) == 0
	if empty && strings.TrimSpace(body.Question) == "" {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "messages or question is required")
		return
	}
	hasText := strings.TrimSpace(body.Question) != ""
	for _, m := range body.Messages {
		if strings.TrimSpace(m.Text) != "" {
			hasText = true
			break
		}
	}
	if !hasText {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "messages or question is required")
		return
	}
	id, err := s.runner.Run(r.Context(), council.CreateRequest{
		ThreadID: body.ThreadID, Messages: body.Messages,
		Question: body.Question, Style: body.Style,
		SupersedesRequestID: body.SupersedesRequestID,
	})
	if err != nil {
		s.log.Warn("create request failed", "thread_id", body.ThreadID, "err", err)
		s.writeErr(w, r, http.StatusBadRequest, "create_failed", "failed to create request")
		return
	}
	s.writeJSON(w, http.StatusAccepted, map[string]any{"request_id": id})
}

func rawMessage(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal(v, &out); err != nil {
		return string(v)
	}
	return out
}

// requestState rebuilds the full request state from persisted rows only.
func (s *Server) requestState(id string) (map[string]any, error) {
	agg, err := s.db.GetRequest(id)
	if err != nil {
		return nil, err
	}
	var snapshot any = agg.Request.SnapshotJSON
	var snapRaw json.RawMessage
	if jerr := json.Unmarshal([]byte(agg.Request.SnapshotJSON), &snapRaw); jerr == nil {
		snapshot = rawMessage(snapRaw)
	}
	views := map[string]any{}
	for _, v := range agg.Views {
		entry := map[string]any{
			"state": v.State, "included": v.IncludedInJudge,
			"included_in_judge": v.IncludedInJudge,
			"late":              v.State == "late",
		}
		if v.CompletedAt != nil {
			entry["completed_at"] = *v.CompletedAt
		}
		if v.ResponseID != nil {
			if resp, rerr := s.db.GetResponse(*v.ResponseID); rerr == nil {
				entry["raw"] = resp.RawText
				entry["raw_text"] = resp.RawText
				entry["parse_ok"] = resp.ParseOK
				entry["timed_out"] = resp.TimedOut
				if resp.ParsedJSON != nil {
					entry["parsed"] = rawMessage(json.RawMessage(*resp.ParsedJSON))
					entry["view"] = rawMessage(json.RawMessage(*resp.ParsedJSON))
				}
				if resp.Error != nil {
					entry["error"] = *resp.Error
				}
			}
		}
		views[v.Seat] = entry
	}
	judge := map[string]any{"state": agg.Judge.State}
	var included, missing []string
	_ = json.Unmarshal([]byte(agg.Judge.IncludedSeatsJSON), &included)
	_ = json.Unmarshal([]byte(agg.Judge.MissingSeatsJSON), &missing)
	judge["included_seats"] = included
	judge["missing_seats"] = missing
	judge["missing"] = missing
	judge["no_independent_views"] = agg.Judge.NoIndependentViews
	if agg.Judge.ResponseID != nil {
		if resp, rerr := s.db.GetResponse(*agg.Judge.ResponseID); rerr == nil {
			judge["raw"] = resp.RawText
			judge["raw_text"] = resp.RawText
			judge["parse_ok"] = resp.ParseOK
			if resp.ParsedJSON != nil {
				judge["parsed"] = rawMessage(json.RawMessage(*resp.ParsedJSON))
				judge["judge"] = rawMessage(json.RawMessage(*resp.ParsedJSON))
			}
		}
	}
	timing := map[string]any{
		"t_first_usable_view_ms": agg.Request.TFirstUsableViewMs,
		"t_final_ms":             agg.Request.TFinalMs,
	}
	return map[string]any{
		"id": id, "request_id": id,
		"thread_id": agg.Request.ThreadID, "pack_id": agg.Request.PackID,
		"state": agg.Request.State, "snapshot": snapshot,
		"views": views, "judge": judge,
		"danger_flagged":         agg.Request.DangerFlagged,
		"danger":                 agg.Request.DangerFlagged,
		"t_first_usable_view_ms": agg.Request.TFirstUsableViewMs,
		"t_final_ms":             agg.Request.TFinalMs,
		"timing":                 timing,
		"timings":                timing,
		"finished_at":            agg.Request.FinishedAt,
		"judge_started_at":       agg.Request.JudgeStartedAt,
	}, nil
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	state, err := s.requestState(id)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") || err == sql.ErrNoRows {
			s.writeErr(w, r, http.StatusNotFound, "not_found", "request not found")
			return
		}
		s.log.Warn("get request failed", "request_id", id, "err", err)
		s.writeErr(w, r, http.StatusNotFound, "not_found", "request not found")
		return
	}
	s.writeJSON(w, http.StatusOK, state)
}

func sseEncode(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetRequestRow(id); err != nil {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "request not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeErr(w, r, http.StatusInternalServerError, "no_sse", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ch, unsub := s.reg.Subscribe(id)
	defer unsub()

	writeEvent := func(event string, payload any) bool {
		_, werr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, sseEncode(payload))
		if werr != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Snapshot first, even when the run left the registry long ago.
	state, err := s.requestState(id)
	if err != nil {
		_ = writeEvent("snapshot", map[string]any{"type": "snapshot", "request_id": id})
	} else {
		if !writeEvent("snapshot", map[string]any{"type": "snapshot", "request_id": id, "snapshot": state, "state": state}) {
			return
		}
	}

	heartbeat := time.NewTicker(SSEHeartbeatInterval)
	defer heartbeat.Stop()
	var grace <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload := map[string]any{
				"type": ev.Type, "request_id": ev.RequestID,
				"at": ev.At.UTC().Format(time.RFC3339),
			}
			switch p := ev.Payload.(type) {
			case map[string]any:
				for k, v := range p {
					payload[k] = v
				}
				payload["payload"] = p
			case map[string]string:
				for k, v := range p {
					payload[k] = v
				}
				payload["payload"] = p
			case nil:
			default:
				payload["payload"] = p
			}
			if !writeEvent(ev.Type, payload) {
				return
			}
			if ev.Type == council.EventDone {
				if grace == nil {
					t := time.NewTimer(SSEDoneGracePeriod)
					defer t.Stop()
					grace = t.C
				}
			}
		case <-grace:
			return
		case <-heartbeat.C:
			if _, werr := fmt.Fprintf(w, ": ping\n\n"); werr != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleRewrite(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		s.writeErr(w, r, http.StatusServiceUnavailable, "no_runner", "request runner unavailable")
		return
	}
	id := r.PathValue("id")
	if _, err := s.db.GetRequestRow(id); err != nil {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "request not found")
		return
	}
	var body struct {
		SourceRef   string `json:"source_ref"`
		Instruction string `json:"instruction"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.SourceRef) == "" || strings.TrimSpace(body.Instruction) == "" {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "source_ref and instruction are required")
		return
	}
	text, rwID, err := s.runner.Rewrite(r.Context(), id, body.SourceRef, body.Instruction)
	if err != nil {
		s.log.Warn("rewrite failed", "request_id", id, "err", err)
		s.writeErr(w, r, http.StatusBadRequest, "rewrite_failed", "rewrite failed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"text": text, "rewrite_id": rwID, "rewrite": text})
}

func (s *Server) handleSent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := s.db.GetRequestRow(id)
	if err != nil {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "request not found")
		return
	}
	var body struct {
		Text      string `json:"text"`
		SourceRef string `json:"source_ref"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "text is required")
		return
	}
	var ref *string
	if strings.TrimSpace(body.SourceRef) != "" {
		ref = &body.SourceRef
	}
	m, err := s.db.InsertSentMessage(id, row.ThreadID, body.Text, ref)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "record sent message failed")
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": m.ID, "sent_at": m.SentAt})
}

func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.db.GetRequestRow(id); err != nil {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "request not found")
		return
	}
	var body struct {
		Tag  string `json:"tag"`
		Note string `json:"note"`
	}
	if !s.decodeBody(w, r, &body) {
		return
	}
	if !validFeedbackTags[body.Tag] {
		s.writeErr(w, r, http.StatusBadRequest, "validation_error", "unknown feedback tag")
		return
	}
	f, err := s.db.InsertFeedback(id, body.Tag, body.Note)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "record feedback failed")
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": f.ID})
}

func (s *Server) handlePackActive(w http.ResponseWriter, r *http.Request) {
	pk, err := pack.Active(s.db)
	if err != nil {
		s.writeErr(w, r, http.StatusNotFound, "not_found", "no active pack")
		return
	}
	seats := map[string]any{}
	for seat, sc := range pk.Seats {
		seats[string(seat)] = map[string]any{
			"model": sc.Model, "family": sc.Family,
			"config_hash": sc.Hash(),
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id": pk.ID, "pack_id": pk.ID, "status": pk.Status,
		"prompt_pack_hash": pk.PromptPackHash, "seats": seats,
	})
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	c := append([]int64(nil), sorted...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	k := int(p * float64(len(c)-1))
	if k < 0 {
		k = 0
	}
	if k >= len(c) {
		k = len(c) - 1
	}
	return c[k]
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func (s *Server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	days := 7
	if q := strings.TrimSpace(r.URL.Query().Get("days")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			s.writeErr(w, r, http.StatusBadRequest, "validation_error", "invalid days parameter")
			return
		}
		days = n
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)

	firstUsable, finals, reqCount, err := s.db.RequestTimings(since)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "metrics query failed")
		return
	}
	views, err := s.db.ViewStateCounts(since)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "metrics query failed")
		return
	}
	judges, err := s.db.JudgeStateCounts(since)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "metrics query failed")
		return
	}
	timeouts, err := s.db.TimeoutCounts(since)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "metrics query failed")
		return
	}
	tags, err := s.db.FeedbackTagCounts(since)
	if err != nil {
		s.writeErr(w, r, http.StatusInternalServerError, "store_error", "metrics query failed")
		return
	}
	viewTotal := sumCounts(views)
	viewRate := 0.0
	if viewTotal > 0 {
		viewRate = float64(views["complete"]) / float64(viewTotal)
	}
	judgeTotal := sumCounts(judges)
	judgeRate := 0.0
	if judgeTotal > 0 {
		judgeRate = float64(judges["complete"]) / float64(judgeTotal)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "request_count": reqCount, "requests": reqCount,
		"t_first_usable_view_ms_p50": percentile(firstUsable, 0.5),
		"t_first_usable_view_ms_p95": percentile(firstUsable, 0.95),
		"t_final_ms_p50":             percentile(finals, 0.5),
		"t_final_ms_p95":             percentile(finals, 0.95),
		"view_completion_rate":       viewRate,
		"judge_completion_rate":      judgeRate,
		"view_states":                views,
		"judge_states":               judges,
		"timeout_counts":             timeouts,
		"timeouts":                   sumCounts(timeouts),
		"feedback_tag_counts":        tags,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	packID := ""
	if pk, err := pack.Active(s.db); err == nil {
		packID = pk.ID
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "db": "ok", "pack_id": packID,
	})
}
